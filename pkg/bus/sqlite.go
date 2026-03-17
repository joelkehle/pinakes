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
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS agents (
	agent_id      TEXT PRIMARY KEY,
	capabilities  TEXT NOT NULL DEFAULT '[]',
	description   TEXT NOT NULL DEFAULT '',
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

	inner := NewStore(cfg)
	s := &SQLiteStore{
		inner: inner,
		db:    db,
	}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load state: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) Close() error {
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
	rows, err := s.db.Query("SELECT agent_id, capabilities, description, mode, callback_url, status, registered_at, expires_at, ttl_seconds FROM agents")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Agent
		var capsJSON, registeredAt, expiresAt string
		if err := rows.Scan(&a.AgentID, &capsJSON, &a.Description, &a.Mode, &a.CallbackURL, &a.Status, &registeredAt, &expiresAt, &a.TTLSeconds); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(capsJSON), &a.Capabilities)
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
		created_at, delivered_at, last_progress_at, ttl_expires_at, grace_until, queued_for_agent
		FROM messages`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var metaJSON sql.NullString
		var attachmentsJSON string
		var createdAt, deliveredAt, lastProgressAt, ttlExpiresAt, graceUntil string
		var queued int
		if err := rows.Scan(&m.MessageID, &m.Type, &m.From, &m.To, &m.ConversationID,
			&m.RequestID, &m.InReplyTo, &m.Body, &metaJSON, &attachmentsJSON, &m.State,
			&createdAt, &deliveredAt, &lastProgressAt, &ttlExpiresAt, &graceUntil, &queued); err != nil {
			return err
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

func (s *SQLiteStore) saveAgent(a *Agent) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO agents (agent_id, capabilities, description, mode, callback_url, status, registered_at, expires_at, ttl_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AgentID,
		marshalJSON(a.Capabilities),
		a.Description,
		string(a.Mode),
		a.CallbackURL,
		string(a.Status),
		timeToString(a.RegisteredAt),
		timeToString(a.ExpiresAt),
		a.TTLSeconds,
	)
	return err
}

func (s *SQLiteStore) saveConversation(c *Conversation) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO conversations (conversation_id, title, participants, status, message_count, created_at, last_message_at, meta)
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

func (s *SQLiteStore) saveMessage(m *Message) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO messages (message_id, type, from_agent, to_agent, conversation_id,
		request_id, in_reply_to, body, meta, attachments, state,
		created_at, delivered_at, last_progress_at, ttl_expires_at, grace_until, queued_for_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		timeToString(m.DeliveredAt),
		timeToString(m.LastProgressAt),
		timeToString(m.TTLExpiresAt),
		timeToString(m.GraceUntil),
		boolToInt(m.QueuedForAgent),
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLiteStore) saveConversationMessage(cid, mid string, position int) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO conversation_messages (conversation_id, message_id, position) VALUES (?, ?, ?)`,
		cid, mid, position)
	return err
}

func (s *SQLiteStore) saveCounters() error {
	s.inner.mu.Lock()
	nextConv := s.inner.nextConversationID
	nextMsg := s.inner.nextMessageID
	s.inner.mu.Unlock()

	_, err := s.db.Exec(`INSERT OR REPLACE INTO counters (key, value) VALUES ('next_conversation_id', ?), ('next_message_id', ?)`,
		nextConv, nextMsg)
	return err
}

// persistAfterSend persists new message, conversation, and counters after SendMessage.
func (s *SQLiteStore) persistAfterSend(m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inner.mu.Lock()
	conv := s.inner.conversations[m.ConversationID]
	convMsgs := s.inner.conversationMessages[m.ConversationID]
	s.inner.mu.Unlock()

	if conv != nil {
		if err := s.saveConversation(conv); err != nil {
			return err
		}
	}
	if err := s.saveMessage(m); err != nil {
		return err
	}
	position := len(convMsgs) - 1
	if err := s.saveConversationMessage(m.ConversationID, m.MessageID, position); err != nil {
		return err
	}
	return s.saveCounters()
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
	if perr := s.saveConversation(out); perr != nil {
		return nil, perr
	}
	if perr := s.saveCounters(); perr != nil {
		return nil, perr
	}
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

// Ensure SQLiteStore satisfies the API interface at compile time.
var _ API = (*SQLiteStore)(nil)
