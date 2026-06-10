package bus

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// SQLiteStore implements bus.API with SQLite-backed persistence.
// It delegates runtime logic (sweep, push callbacks, observe events, inbox buffering)
// to an embedded in-memory Store, and persists the core entities (agents,
// conversations, messages) to SQLite with write-through semantics.
// Transient data (inboxes, observe events, idempotency) stays in-memory only.
type SQLiteStore struct {
	inner *Store
	db    *sqlx.DB
	mu    sync.Mutex

	// pruneStop terminates the background DB-prune goroutine on Close.
	pruneStop chan struct{}
	pruneOnce sync.Once

	// testHookBeforeCommit, if non-nil, is invoked inside persistAfterSend
	// and CreateConversation's transaction right before Commit. Returning a
	// non-nil error forces a rollback so tests can prove all-or-nothing
	// semantics. Production callers never set this.
	testHookBeforeCommit func() error
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS agents (
	agent_id      TEXT PRIMARY KEY,
	capabilities  TEXT NOT NULL DEFAULT '[]',
	secret        TEXT NOT NULL DEFAULT '',
	version       TEXT NOT NULL DEFAULT '',
	description   TEXT NOT NULL DEFAULT '',
	agent_class   TEXT NOT NULL DEFAULT '',
	mutation_class TEXT NOT NULL DEFAULT '',
	build         TEXT,
	meta          TEXT,
	mode          TEXT NOT NULL DEFAULT 'pull',
	callback_url  TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'active',
	registered_at TEXT NOT NULL,
	expires_at    TEXT NOT NULL,
	ttl_seconds   INTEGER NOT NULL DEFAULT 60
);

CREATE TABLE IF NOT EXISTS conversations (
	conversation_id TEXT PRIMARY KEY,
	title           TEXT NOT NULL DEFAULT '',
	participants    TEXT NOT NULL DEFAULT '[]',
	status          TEXT NOT NULL DEFAULT 'active',
	message_count   INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	last_message_at TEXT NOT NULL,
	meta            TEXT
);

CREATE TABLE IF NOT EXISTS messages (
	message_id       TEXT PRIMARY KEY,
	type             TEXT NOT NULL,
	from_agent       TEXT NOT NULL,
	to_agent         TEXT NOT NULL DEFAULT '',
	conversation_id  TEXT NOT NULL DEFAULT '',
	request_id       TEXT NOT NULL DEFAULT '',
	in_reply_to      TEXT NOT NULL DEFAULT '',
	body             TEXT NOT NULL DEFAULT '',
	meta             TEXT,
	attachments      TEXT NOT NULL DEFAULT '[]',
	state            TEXT NOT NULL DEFAULT 'pending',
	created_at       TEXT NOT NULL,
	terminal_at      TEXT NOT NULL DEFAULT '',
	delivered_at     TEXT NOT NULL DEFAULT '',
	last_progress_at TEXT NOT NULL DEFAULT '',
	ttl_expires_at   TEXT NOT NULL DEFAULT '',
	grace_until      TEXT NOT NULL DEFAULT '',
	queued_for_agent INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS conversation_messages (
	conversation_id TEXT NOT NULL,
	message_id      TEXT NOT NULL,
	position        INTEGER NOT NULL,
	PRIMARY KEY (conversation_id, position)
);

CREATE TABLE IF NOT EXISTS counters (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL DEFAULT 0
);
`

func NewSQLiteStore(dbPath string, cfg Config) (*SQLiteStore, error) {
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := ensureAgentColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate agent schema: %w", err)
	}
	if err := ensureMessageColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate message schema: %w", err)
	}

	inner := NewStore(cfg)
	s := &SQLiteStore{
		inner:     inner,
		db:        db,
		pruneStop: make(chan struct{}),
	}

	// Prune before loading so a DB that grew past retention while the bus was
	// down (or before retention existed) cannot re-inflate memory on restart.
	if err := s.pruneDB(inner.now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("prune state: %w", err)
	}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load state: %w", err)
	}

	go s.pruneLoop()

	return s, nil
}

// pruneLoop mirrors the in-memory retention sweep into SQLite so the DB stays
// bounded too. The in-memory store prunes itself; this only deletes rows.
func (s *SQLiteStore) pruneLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.pruneStop:
			return
		case <-ticker.C:
			_ = s.pruneDB(s.inner.now())
		}
	}
}

// pruneDB deletes rows past retention. RFC3339 strings compare
// lexicographically with at-most sub-second error at the cutoff, which is
// irrelevant for hour-scale retention windows.
func (s *SQLiteStore) pruneDB(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.inner.cfg
	if cfg.MessageMaxAge > 0 {
		cutoff := timeToString(now.Add(-cfg.MessageMaxAge))
		if _, err := s.db.Exec(`DELETE FROM messages WHERE created_at <> '' AND created_at < ?`, cutoff); err != nil {
			return err
		}
	}
	if cfg.MessageRetention > 0 {
		cutoff := timeToString(now.Add(-cfg.MessageRetention))
		if _, err := s.db.Exec(`DELETE FROM messages
			WHERE state IN ('completed', 'rejected', 'error')
			AND (CASE WHEN terminal_at <> '' THEN terminal_at ELSE created_at END) < ?`, cutoff); err != nil {
			return err
		}
	}
	if cfg.ConversationRetention > 0 {
		cutoff := timeToString(now.Add(-cfg.ConversationRetention))
		if _, err := s.db.Exec(`DELETE FROM conversations
			WHERE last_message_at <> '' AND last_message_at < ?
			AND conversation_id NOT IN (SELECT DISTINCT conversation_id FROM messages)`, cutoff); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM conversation_messages
		WHERE message_id NOT IN (SELECT message_id FROM messages)`); err != nil {
		return err
	}
	return nil
}

func ensureAgentColumns(db *sqlx.DB) error {
	rows, err := db.Query("PRAGMA table_info(agents)")
	if err != nil {
		return err
	}
	defer rows.Close()

	cols := map[string]struct{}{}
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &primaryKey); err != nil {
			return err
		}
		cols[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	migrations := []struct {
		name string
		sql  string
	}{
		{name: "secret", sql: `ALTER TABLE agents ADD COLUMN secret TEXT NOT NULL DEFAULT ''`},
		{name: "version", sql: `ALTER TABLE agents ADD COLUMN version TEXT NOT NULL DEFAULT ''`},
		{name: "agent_class", sql: `ALTER TABLE agents ADD COLUMN agent_class TEXT NOT NULL DEFAULT ''`},
		{name: "mutation_class", sql: `ALTER TABLE agents ADD COLUMN mutation_class TEXT NOT NULL DEFAULT ''`},
		{name: "build", sql: `ALTER TABLE agents ADD COLUMN build TEXT`},
		{name: "meta", sql: `ALTER TABLE agents ADD COLUMN meta TEXT`},
	}
	for _, migration := range migrations {
		if _, ok := cols[migration.name]; ok {
			continue
		}
		if _, err := db.Exec(migration.sql); err != nil {
			return err
		}
	}
	return nil
}

func ensureMessageColumns(db *sqlx.DB) error {
	rows, err := db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return err
	}
	defer rows.Close()

	cols := map[string]struct{}{}
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &primaryKey); err != nil {
			return err
		}
		cols[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if _, ok := cols["terminal_at"]; !ok {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN terminal_at TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	s.pruneOnce.Do(func() { close(s.pruneStop) })
	return s.db.Close()
}

// --- load all state from SQLite into the in-memory Store ---

func (s *SQLiteStore) loadAll() error {
	if err := s.loadCounters(); err != nil {
		return err
	}
	if err := s.loadAgents(); err != nil {
		return err
	}
	if err := s.loadConversations(); err != nil {
		return err
	}
	if err := s.loadMessages(); err != nil {
		return err
	}
	if err := s.loadConversationMessages(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) loadCounters() error {
	rows, err := s.db.Query("SELECT key, value FROM counters")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		switch key {
		case "next_conversation_id":
			s.inner.nextConversationID = value
		case "next_message_id":
			s.inner.nextMessageID = value
		}
	}
	return rows.Err()
}

func (s *SQLiteStore) loadAgents() error {
	rows, err := s.db.Query("SELECT agent_id, capabilities, version, description, agent_class, mutation_class, build, meta, mode, callback_url, status, registered_at, expires_at, ttl_seconds FROM agents")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Agent
		var capsJSON, registeredAt, expiresAt string
		var buildJSON, metaJSON sql.NullString
		if err := rows.Scan(&a.AgentID, &capsJSON, &a.Version, &a.Description, &a.AgentClass, &a.MutationClass, &buildJSON, &metaJSON, &a.Mode, &a.CallbackURL, &a.Status, &registeredAt, &expiresAt, &a.TTLSeconds); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(capsJSON), &a.Capabilities)
		if buildJSON.Valid && buildJSON.String != "" {
			a.Build = &BuildInfo{}
			_ = json.Unmarshal([]byte(buildJSON.String), a.Build)
		}
		if metaJSON.Valid && metaJSON.String != "" {
			a.Meta = &AgentMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), a.Meta)
		}
		a.RegisteredAt, _ = time.Parse(time.RFC3339Nano, registeredAt)
		a.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
		s.inner.agents[a.AgentID] = &a
		if _, ok := s.inner.inboxes[a.AgentID]; !ok {
			s.inner.inboxes[a.AgentID] = []InboxEvent{}
			s.inner.inboxBase[a.AgentID] = 0
		}
	}
	return rows.Err()
}

func (s *SQLiteStore) loadConversations() error {
	rows, err := s.db.Query("SELECT conversation_id, title, participants, status, message_count, created_at, last_message_at, meta FROM conversations")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Conversation
		var participantsJSON, createdAt, lastMessageAt string
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ConversationID, &c.Title, &participantsJSON, &c.Status, &c.MessageCount, &createdAt, &lastMessageAt, &metaJSON); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(participantsJSON), &c.Participants)
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		c.LastMessageAt, _ = time.Parse(time.RFC3339Nano, lastMessageAt)
		if metaJSON.Valid && metaJSON.String != "" {
			_ = json.Unmarshal([]byte(metaJSON.String), &c.Meta)
		}
		s.inner.conversations[c.ConversationID] = &c
	}
	return rows.Err()
}

func (s *SQLiteStore) loadMessages() error {
	rows, err := s.db.Query(`SELECT message_id, type, from_agent, to_agent, conversation_id,
		request_id, in_reply_to, body, meta, attachments, state,
		created_at, terminal_at, delivered_at, last_progress_at, ttl_expires_at, grace_until, queued_for_agent
		FROM messages`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var metaJSON sql.NullString
		var attachmentsJSON string
		var createdAt, terminalAt, deliveredAt, lastProgressAt, ttlExpiresAt, graceUntil string
		var queued int
		if err := rows.Scan(&m.MessageID, &m.Type, &m.From, &m.To, &m.ConversationID,
			&m.RequestID, &m.InReplyTo, &m.Body, &metaJSON, &attachmentsJSON, &m.State,
			&createdAt, &terminalAt, &deliveredAt, &lastProgressAt, &ttlExpiresAt, &graceUntil, &queued); err != nil {
			return err
		}
		if terminalAt != "" {
			m.TerminalAt, _ = time.Parse(time.RFC3339Nano, terminalAt)
		}
		if metaJSON.Valid && metaJSON.String != "" {
			_ = json.Unmarshal([]byte(metaJSON.String), &m.Meta)
		}
		_ = json.Unmarshal([]byte(attachmentsJSON), &m.Attachments)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if deliveredAt != "" {
			m.DeliveredAt, _ = time.Parse(time.RFC3339Nano, deliveredAt)
		}
		if lastProgressAt != "" {
			m.LastProgressAt, _ = time.Parse(time.RFC3339Nano, lastProgressAt)
		}
		if ttlExpiresAt != "" {
			m.TTLExpiresAt, _ = time.Parse(time.RFC3339Nano, ttlExpiresAt)
		}
		if graceUntil != "" {
			m.GraceUntil, _ = time.Parse(time.RFC3339Nano, graceUntil)
		}
		m.QueuedForAgent = queued != 0
		s.inner.messages[m.MessageID] = &m
	}
	return rows.Err()
}

func (s *SQLiteStore) loadConversationMessages() error {
	rows, err := s.db.Query("SELECT conversation_id, message_id FROM conversation_messages ORDER BY conversation_id, position")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, mid string
		if err := rows.Scan(&cid, &mid); err != nil {
			return err
		}
		s.inner.conversationMessages[cid] = append(s.inner.conversationMessages[cid], mid)
	}
	return rows.Err()
}

// --- persist helpers ---

func timeToString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func marshalJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func nullableJSON(v any) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// sqliteExec abstracts over *sqlx.DB and *sqlx.Tx so that the save* helpers
// can write either directly (auto-commit) or inside a multi-statement
// transaction.
type sqliteExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func saveAgentTo(exec sqliteExec, a *Agent) error {
	_, err := exec.Exec(`INSERT INTO agents (agent_id, capabilities, version, description, agent_class, mutation_class, build, meta, mode, callback_url, status, registered_at, expires_at, ttl_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			capabilities=excluded.capabilities,
			version=excluded.version,
			description=excluded.description,
			agent_class=excluded.agent_class,
			mutation_class=excluded.mutation_class,
			build=excluded.build,
			meta=excluded.meta,
			mode=excluded.mode,
			callback_url=excluded.callback_url,
			status=excluded.status,
			registered_at=excluded.registered_at,
			expires_at=excluded.expires_at,
			ttl_seconds=excluded.ttl_seconds`,
		a.AgentID,
		marshalJSON(a.Capabilities),
		a.Version,
		a.Description,
		a.AgentClass,
		a.MutationClass,
		nullableJSON(a.Build),
		nullableJSON(a.Meta),
		string(a.Mode),
		a.CallbackURL,
		string(a.Status),
		timeToString(a.RegisteredAt),
		timeToString(a.ExpiresAt),
		a.TTLSeconds,
	)
	return err
}

func (s *SQLiteStore) saveAgent(a *Agent) error {
	return saveAgentTo(s.db, a)
}

func saveConversationTo(exec sqliteExec, c *Conversation) error {
	_, err := exec.Exec(`INSERT OR REPLACE INTO conversations (conversation_id, title, participants, status, message_count, created_at, last_message_at, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ConversationID,
		c.Title,
		marshalJSON(c.Participants),
		c.Status,
		c.MessageCount,
		timeToString(c.CreatedAt),
		timeToString(c.LastMessageAt),
		nullableJSON(c.Meta),
	)
	return err
}

func (s *SQLiteStore) saveConversation(c *Conversation) error {
	return saveConversationTo(s.db, c)
}

func saveMessageTo(exec sqliteExec, m *Message) error {
	_, err := exec.Exec(`INSERT OR REPLACE INTO messages (message_id, type, from_agent, to_agent, conversation_id,
		request_id, in_reply_to, body, meta, attachments, state,
		created_at, terminal_at, delivered_at, last_progress_at, ttl_expires_at, grace_until, queued_for_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageID,
		string(m.Type),
		m.From,
		m.To,
		m.ConversationID,
		m.RequestID,
		m.InReplyTo,
		m.Body,
		nullableJSON(m.Meta),
		marshalJSON(m.Attachments),
		string(m.State),
		timeToString(m.CreatedAt),
		timeToString(m.TerminalAt),
		timeToString(m.DeliveredAt),
		timeToString(m.LastProgressAt),
		timeToString(m.TTLExpiresAt),
		timeToString(m.GraceUntil),
		boolToInt(m.QueuedForAgent),
	)
	return err
}

func (s *SQLiteStore) saveMessage(m *Message) error {
	return saveMessageTo(s.db, m)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func saveConversationMessageTo(exec sqliteExec, cid, mid string, position int) error {
	_, err := exec.Exec(`INSERT OR REPLACE INTO conversation_messages (conversation_id, message_id, position) VALUES (?, ?, ?)`,
		cid, mid, position)
	return err
}

func saveCountersTo(exec sqliteExec, nextConv, nextMsg int64) error {
	_, err := exec.Exec(`INSERT OR REPLACE INTO counters (key, value) VALUES ('next_conversation_id', ?), ('next_message_id', ?)`,
		nextConv, nextMsg)
	return err
}

func (s *SQLiteStore) saveCounters() error {
	s.inner.mu.Lock()
	nextConv := s.inner.nextConversationID
	nextMsg := s.inner.nextMessageID
	s.inner.mu.Unlock()
	return saveCountersTo(s.db, nextConv, nextMsg)
}

// persistAfterSend persists the message row, its parent conversation, the
// conversation_messages position link, and the counter advance from a single
// SendMessage call. All four writes go through one SQL transaction so that a
// crash mid-persist cannot leave a counter advanced past an unwritten row, a
// message without its conversation, or a conversation without its position
// link. Pre-v0.2.2 these were four separate auto-commit Execs, which produced
// the conversation-count drift observed on bus bounce.
func (s *SQLiteStore) persistAfterSend(m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inner.mu.Lock()
	var convCopy *Conversation
	if c := s.inner.conversations[m.ConversationID]; c != nil {
		cp := *c
		convCopy = &cp
	}
	convMsgs := s.inner.conversationMessages[m.ConversationID]
	nextConv := s.inner.nextConversationID
	nextMsg := s.inner.nextMessageID
	s.inner.mu.Unlock()

	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if convCopy != nil {
		if err := saveConversationTo(tx, convCopy); err != nil {
			return err
		}
	}
	if err := saveMessageTo(tx, m); err != nil {
		return err
	}
	position := len(convMsgs) - 1
	if err := saveConversationMessageTo(tx, m.ConversationID, m.MessageID, position); err != nil {
		return err
	}
	if err := saveCountersTo(tx, nextConv, nextMsg); err != nil {
		return err
	}
	if hook := s.testHookBeforeCommit; hook != nil {
		if err := hook(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// persistMessageState saves just the message row (state change after ack/event).
func (s *SQLiteStore) persistMessageState(messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inner.mu.Lock()
	m, ok := s.inner.messages[messageID]
	if !ok {
		s.inner.mu.Unlock()
		return nil
	}
	cp := *m
	s.inner.mu.Unlock()

	return s.saveMessage(&cp)
}

// --- bus.API implementation ---

func (s *SQLiteStore) RegisterAgent(input RegisterAgentInput) (*Agent, error) {
	out, err := s.inner.RegisterAgent(input)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if perr := s.saveAgent(out); perr != nil {
		return nil, perr
	}
	return out, nil
}

func (s *SQLiteStore) AgentSecrets() (map[string]string, error) {
	rows, err := s.db.Query("SELECT agent_id, secret FROM agents WHERE secret <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var agentID, secret string
		if err := rows.Scan(&agentID, &secret); err != nil {
			return nil, err
		}
		agentID = strings.TrimSpace(agentID)
		if agentID != "" && strings.TrimSpace(secret) != "" {
			out[agentID] = secret
		}
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetAgentSecret(agentID, secret string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || strings.TrimSpace(secret) == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("UPDATE agents SET secret = ? WHERE agent_id = ?", secret, agentID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent %s not found", agentID)
	}
	return nil
}

func (s *SQLiteStore) ListAgents(capability string) []Agent {
	return s.inner.ListAgents(capability)
}

func (s *SQLiteStore) CreateConversation(input CreateConversationInput) (*Conversation, error) {
	out, err := s.inner.CreateConversation(input)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inner.mu.Lock()
	nextConv := s.inner.nextConversationID
	nextMsg := s.inner.nextMessageID
	s.inner.mu.Unlock()

	tx, err := s.db.Beginx()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if perr := saveConversationTo(tx, out); perr != nil {
		return nil, perr
	}
	if perr := saveCountersTo(tx, nextConv, nextMsg); perr != nil {
		return nil, perr
	}
	if hook := s.testHookBeforeCommit; hook != nil {
		if err := hook(); err != nil {
			return nil, err
		}
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	committed = true
	return out, nil
}

func (s *SQLiteStore) ListConversations(filter ListConversationsFilter) []Conversation {
	return s.inner.ListConversations(filter)
}

func (s *SQLiteStore) SendMessage(input SendMessageInput) (*Message, bool, error) {
	m, dup, err := s.inner.SendMessage(input)
	if err != nil {
		return nil, false, err
	}
	if !dup {
		if perr := s.persistAfterSend(m); perr != nil {
			return nil, false, perr
		}
	}
	return m, dup, nil
}

func (s *SQLiteStore) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	return s.inner.PollInbox(input)
}

func (s *SQLiteStore) Ack(input AckInput) error {
	err := s.inner.Ack(input)
	if err != nil {
		return err
	}
	messageID := strings.TrimSpace(input.MessageID)
	if perr := s.persistMessageState(messageID); perr != nil {
		return perr
	}
	return nil
}

func (s *SQLiteStore) PostEvent(input EventInput) error {
	err := s.inner.PostEvent(input)
	if err != nil {
		return err
	}
	messageID := strings.TrimSpace(input.MessageID)
	if perr := s.persistMessageState(messageID); perr != nil {
		return perr
	}
	return nil
}

func (s *SQLiteStore) Inject(input InjectInput) (*Message, error) {
	m, err := s.inner.Inject(input)
	if err != nil {
		return nil, err
	}
	if perr := s.persistAfterSend(m); perr != nil {
		return nil, perr
	}
	return m, nil
}

func (s *SQLiteStore) ListConversationMessages(input ListConversationMessagesInput) (string, []Message, int, error) {
	return s.inner.ListConversationMessages(input)
}

func (s *SQLiteStore) ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64) {
	return s.inner.ObserveSince(afterID, filter, wait)
}

func (s *SQLiteStore) Health() map[string]any {
	return s.inner.Health()
}

func (s *SQLiteStore) Metrics() string {
	return s.inner.Metrics()
}

func (s *SQLiteStore) SystemStatus() map[string]any {
	return s.inner.SystemStatus()
}

func (s *SQLiteStore) GetMessageForTest(messageID string) (Message, bool) {
	return s.inner.GetMessageForTest(messageID)
}

// Ensure SQLiteStore satisfies the API interfaces at compile time.
var _ API = (*SQLiteStore)(nil)
var _ AgentSecretStore = (*SQLiteStore)(nil)
