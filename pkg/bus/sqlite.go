package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// SQLiteStore implements bus.API with SQLite-backed persistence.
// It delegates runtime logic to an embedded in-memory Store while SQLite owns
// accepted messages, delivery state, pull cursors, and duplicate receipts.
// Observer events remain process-local and transient.
type SQLiteStore struct {
	inner *Store
	db    *sqlx.DB
	mu    sync.Mutex

	// pruneStop terminates the background DB-prune goroutine on Close.
	pruneStop    chan struct{}
	closeOnce    sync.Once
	closeErr     error
	backgroundWG sync.WaitGroup

	// deliveryPersistErrors keeps callback receipt write failures visible per
	// delivery until that same delivery succeeds or shutdown recovery requeues
	// all attempting rows.
	deliveryPersistErrors map[string]error

	// testHookBeforeCommit, if non-nil, is invoked inside persistAfterSend
	// and CreateConversation's transaction right before Commit. Returning a
	// non-nil error forces a rollback so tests can prove all-or-nothing
	// semantics. Production callers never set this.
	testHookBeforeCommit func() error

	// testHookPushReceipt, if non-nil, runs at the start of recordPushFailure
	// before its receipt write (and while holding no lock). Tests use it to hold
	// a delivery receipt write open and prove the send response does not wait on
	// it. Production callers never set this.
	testHookPushReceipt func()
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS agents (
	agent_id      TEXT PRIMARY KEY,
	allowed_scopes TEXT NOT NULL DEFAULT '[]',
	shared_grants TEXT NOT NULL DEFAULT '[]',
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

	CREATE TABLE IF NOT EXISTS delivery_cursors (
		target_agent_id    TEXT PRIMARY KEY,
		next_seq           INTEGER NOT NULL DEFAULT 0,
		acknowledged_cursor INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS deliveries (
		target_agent_id TEXT NOT NULL,
		delivery_seq    INTEGER NOT NULL,
		message_id      TEXT NOT NULL UNIQUE,
		status          TEXT NOT NULL DEFAULT 'pending',
		next_attempt_at TEXT NOT NULL DEFAULT '',
		attempt_count   INTEGER NOT NULL DEFAULT 0,
		received_at     TEXT NOT NULL DEFAULT '',
		expires_at      TEXT NOT NULL,
		last_error      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (target_agent_id, delivery_seq),
		FOREIGN KEY (message_id) REFERENCES messages(message_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS deliveries_pending_idx
		ON deliveries(status, next_attempt_at);

	CREATE TABLE IF NOT EXISTS idempotency (
		from_agent TEXT NOT NULL,
		to_agent   TEXT NOT NULL,
		request_id TEXT NOT NULL,
		message_id TEXT NOT NULL,
		accepted_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		PRIMARY KEY (from_agent, to_agent, request_id)
	);

	CREATE TABLE IF NOT EXISTS schema_migrations (
		key        TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	);
	`

func NewSQLiteStore(dbPath string, cfg Config) (*SQLiteStore, error) {
	inner, err := NewStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid store configuration: %w", err)
	}
	storeReady := false
	defer func() {
		if !storeReady {
			_ = inner.closePushWorkers()
		}
	}()

	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite db dir: %w", err)
		}
	}

	// journal_mode=WAL is deliberately not set via the connection-string
	// _pragma option here: that applies at connect time, before we get a
	// chance to run our own PRAGMA auto_vacuum below, and switching into WAL
	// mode writes the database's page 1 immediately (even with zero tables).
	// auto_vacuum only takes effect without a VACUUM when the database is
	// still completely empty, so it must run first against a database that
	// has never been written to; setting it after the WAL switch silently
	// no-ops. This ordering is the actual fix for issue #29.
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	// Only actually converts brand-new database files (see comment above for
	// why). An existing file created before this line was added keeps
	// auto_vacuum=NONE until an operator runs `pinakes-db-compact` (see
	// compact.go) during a maintenance window.
	if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set journal_mode: %w", err)
	}

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

	s := &SQLiteStore{
		inner:                 inner,
		db:                    db,
		pruneStop:             make(chan struct{}),
		deliveryPersistErrors: map[string]error{},
	}
	if err := s.backfillLegacyDeliveryState(); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill durable delivery state: %w", err)
	}
	if _, err := db.Exec(`UPDATE deliveries SET status = 'pending'
		WHERE status = 'attempting'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover push deliveries: %w", err)
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

	s.backgroundWG.Add(2)
	go func() {
		defer s.backgroundWG.Done()
		s.pruneLoop()
	}()
	go func() {
		defer s.backgroundWG.Done()
		s.deliveryLoop()
	}()

	storeReady = true
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
	nowString := timeToString(now)
	if _, err := s.db.Exec(`UPDATE deliveries SET status = 'failed',
		next_attempt_at = '', last_error = 'transport deadline expired'
		WHERE status IN ('pending', 'attempting') AND expires_at <= ?`, nowString); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM idempotency WHERE expires_at < ?`, nowString); err != nil {
		return err
	}
	if cfg.MessageMaxAge > 0 {
		cutoff := timeToString(now.Add(-cfg.MessageMaxAge))
		if _, err := s.db.Exec(`DELETE FROM messages
			WHERE created_at <> '' AND created_at < ?
			AND message_id NOT IN (
				SELECT message_id FROM deliveries WHERE status IN ('pending', 'attempting')
			)`, cutoff); err != nil {
			return err
		}
	}
	if cfg.MessageRetention > 0 {
		cutoff := timeToString(now.Add(-cfg.MessageRetention))
		if _, err := s.db.Exec(`DELETE FROM messages
				WHERE state IN ('completed', 'rejected', 'error')
				AND (CASE WHEN terminal_at <> '' THEN terminal_at ELSE created_at END) < ?
				AND message_id NOT IN (
					SELECT message_id FROM deliveries WHERE status IN ('pending', 'attempting')
				)`, cutoff); err != nil {
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

	// Reclaim freed pages back to the OS as we go, instead of only ever
	// growing the freelist (issue #29: a 1.17 GB file with 99.86% free
	// pages). This is a no-op until the database is in incremental
	// auto_vacuum mode (fresh DBs get that on creation above; existing DBs
	// need a one-time `pinakes-db-compact` to convert, see compact.go). The
	// page cap keeps each prune sweep bounded instead of vacuuming the whole
	// freelist in one call.
	return runIncrementalVacuum(s.db, 1000)
}

// runIncrementalVacuum reclaims up to maxPages free pages. PRAGMA
// incremental_vacuum(N) is implemented as a SQLite virtual table that frees
// one page per row produced, so a plain Exec (which only asks the driver to
// step the statement once) reclaims exactly one page and silently leaves the
// rest; the pragma must be driven with Query and the result rows drained to
// actually free all N pages. This is a no-op if the database is not in
// incremental auto_vacuum mode.
func runIncrementalVacuum(exec sqliteQueryer, maxPages int) error {
	rows, err := exec.Query(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", maxPages))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// sqliteQueryer is satisfied by *sqlx.DB; kept minimal so tests can pass a
// bare *sql.DB-like value if ever needed.
type sqliteQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
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
		{name: "allowed_scopes", sql: `ALTER TABLE agents ADD COLUMN allowed_scopes TEXT NOT NULL DEFAULT '[]'`},
		{name: "shared_grants", sql: `ALTER TABLE agents ADD COLUMN shared_grants TEXT NOT NULL DEFAULT '[]'`},
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
	s.closeOnce.Do(func() {
		close(s.pruneStop)
		s.backgroundWG.Wait()
		pushErr := s.inner.closePushWorkers()

		s.mu.Lock()
		_, recoveryErr := s.db.Exec(`UPDATE deliveries SET status = 'pending',
			next_attempt_at = CASE WHEN next_attempt_at = '' THEN ? ELSE next_attempt_at END,
			last_error = CASE WHEN last_error = '' THEN 'shutdown recovery' ELSE last_error END
			WHERE status = 'attempting'`, timeToString(s.inner.now()))
		if recoveryErr == nil {
			clear(s.deliveryPersistErrors)
		}
		s.mu.Unlock()

		s.closeErr = errors.Join(pushErr, recoveryErr, s.db.Close())
	})
	return s.closeErr
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
	if err := s.loadIdempotency(); err != nil {
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
	rows, err := s.db.Query("SELECT agent_id, allowed_scopes, shared_grants, capabilities, version, description, agent_class, mutation_class, build, meta, mode, callback_url, status, registered_at, expires_at, ttl_seconds FROM agents")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Agent
		var scopesJSON, grantsJSON, capsJSON, registeredAt, expiresAt string
		var buildJSON, metaJSON sql.NullString
		if err := rows.Scan(&a.AgentID, &scopesJSON, &grantsJSON, &capsJSON, &a.Version, &a.Description, &a.AgentClass, &a.MutationClass, &buildJSON, &metaJSON, &a.Mode, &a.CallbackURL, &a.Status, &registeredAt, &expiresAt, &a.TTLSeconds); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(scopesJSON), &a.AllowedScopes)
		_ = json.Unmarshal([]byte(grantsJSON), &a.SharedGrants)
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
		m, err := scanSQLiteMessage(rows)
		if err != nil {
			return err
		}
		s.inner.messages[m.MessageID] = &m
	}
	return rows.Err()
}

type sqliteScanner interface {
	Scan(dest ...any) error
}

func scanSQLiteMessage(scanner sqliteScanner, prefix ...any) (Message, error) {
	var m Message
	var metaJSON sql.NullString
	var attachmentsJSON string
	var createdAt, terminalAt, deliveredAt, lastProgressAt, ttlExpiresAt, graceUntil string
	var queued int
	dest := append(prefix,
		&m.MessageID, &m.Type, &m.From, &m.To, &m.ConversationID,
		&m.RequestID, &m.InReplyTo, &m.Body, &metaJSON, &attachmentsJSON, &m.State,
		&createdAt, &terminalAt, &deliveredAt, &lastProgressAt, &ttlExpiresAt, &graceUntil, &queued,
	)
	if err := scanner.Scan(dest...); err != nil {
		return Message{}, err
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
	return m, nil
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
	_, err := exec.Exec(`INSERT INTO agents (agent_id, allowed_scopes, shared_grants, capabilities, version, description, agent_class, mutation_class, build, meta, mode, callback_url, status, registered_at, expires_at, ttl_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			allowed_scopes=excluded.allowed_scopes,
			shared_grants=excluded.shared_grants,
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
		marshalJSON(a.AllowedScopes),
		marshalJSON(a.SharedGrants),
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
	_, err := exec.Exec(`INSERT INTO messages (message_id, type, from_agent, to_agent, conversation_id,
		request_id, in_reply_to, body, meta, attachments, state,
		created_at, terminal_at, delivered_at, last_progress_at, ttl_expires_at, grace_until, queued_for_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET
			type = excluded.type,
			from_agent = excluded.from_agent,
			to_agent = excluded.to_agent,
			conversation_id = excluded.conversation_id,
			request_id = excluded.request_id,
			in_reply_to = excluded.in_reply_to,
			body = excluded.body,
			meta = excluded.meta,
			attachments = excluded.attachments,
			state = excluded.state,
			created_at = excluded.created_at,
			terminal_at = excluded.terminal_at,
			delivered_at = excluded.delivered_at,
			last_progress_at = excluded.last_progress_at,
			ttl_expires_at = excluded.ttl_expires_at,
			grace_until = excluded.grace_until,
			queued_for_agent = excluded.queued_for_agent`,
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
	s.inner.mu.Lock()
	nextConv := s.inner.nextConversationID
	nextMsg := s.inner.nextMessageID
	s.inner.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

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
	return s.inner.sendMessage(input, s.persistAcceptance)
}

func (s *SQLiteStore) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	return s.pollDurableInbox(input)
}

func (s *SQLiteStore) Ack(input AckInput) error {
	return s.inner.ack(input, s.persistAck)
}

func (s *SQLiteStore) PostEvent(input EventInput) error {
	return s.inner.postEvent(input, s.persistAck)
}

func (s *SQLiteStore) Inject(input InjectInput) (*Message, error) {
	return s.inner.inject(input, s.persistAcceptance)
}

func (s *SQLiteStore) ListConversationMessages(input ListConversationMessagesInput) (string, []Message, int, error) {
	return s.inner.ListConversationMessages(input)
}

func (s *SQLiteStore) ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64) {
	return s.inner.ObserveSince(afterID, filter, wait)
}

func (s *SQLiteStore) ObserveEpoch() string {
	return s.inner.ObserveEpoch()
}

func (s *SQLiteStore) Health() map[string]any {
	out := s.inner.Health()
	pending, failed := s.deliveryCounts()
	out["pending_deliveries"] = pending
	out["failed_deliveries"] = failed
	s.mu.Lock()
	persistErrors := len(s.deliveryPersistErrors)
	s.mu.Unlock()
	if persistErrors > 0 {
		out["ok"] = false
		out["status"] = "degraded"
		out["delivery_persistence_error"] = true
	}
	return out
}

func (s *SQLiteStore) Metrics() string {
	pending, failed := s.deliveryCounts()
	return s.inner.Metrics() + fmt.Sprintf(
		"# HELP agent_bus_pending_deliveries Durable deliveries awaiting transport receipt.\n"+
			"# TYPE agent_bus_pending_deliveries gauge\n"+
			"agent_bus_pending_deliveries %d\n"+
			"# HELP agent_bus_failed_deliveries Durable deliveries that reached their transport deadline.\n"+
			"# TYPE agent_bus_failed_deliveries gauge\n"+
			"agent_bus_failed_deliveries %d\n",
		pending, failed)
}

func (s *SQLiteStore) SystemStatus() map[string]any {
	out := s.inner.SystemStatus()
	pending, failed := s.deliveryCounts()
	out["pending_deliveries"] = pending
	out["failed_deliveries"] = failed
	return out
}

func (s *SQLiteStore) GetMessageForTest(messageID string) (Message, bool) {
	return s.inner.GetMessageForTest(messageID)
}

// Ensure SQLiteStore satisfies the API interfaces at compile time.
var _ API = (*SQLiteStore)(nil)
var _ AgentSecretStore = (*SQLiteStore)(nil)
var _ ObserveEpochProvider = (*SQLiteStore)(nil)
