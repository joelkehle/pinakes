package bus

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSQLiteStartupPrune asserts that rows past retention are deleted before
// loadAll, so a DB that grew while the bus was down cannot re-inflate memory
// on restart.
func TestSQLiteStartupPrune(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	cfg := Config{
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		MessageRetention:       time.Hour,
		MessageMaxAge:          12 * time.Hour,
		ConversationRetention:  2 * time.Hour,
		Clock:                  clock,
	}

	s, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	for _, id := range []string{"ucla.a", "ucla.b"} {
		if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: id, Mode: AgentModePull, TTLSeconds: 60}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	msg, _, err := s.SendMessage(SendMessageInput{
		To: "ucla.b", From: "ucla.a", RequestID: "r1", Type: MessageTypeRequest, Body: "old work",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen far past MessageMaxAge: the startup prune must drop the row
	// before it is loaded into memory.
	mu.Lock()
	current = current.Add(25 * time.Hour)
	mu.Unlock()
	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen sqlite store: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.GetMessageForTest(msg.MessageID); ok {
		t.Fatalf("expected message pruned at startup, but it was loaded")
	}
	var msgRows, convRows, linkRows int
	if err := s2.db.Get(&msgRows, "SELECT COUNT(*) FROM messages"); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := s2.db.Get(&convRows, "SELECT COUNT(*) FROM conversations"); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if err := s2.db.Get(&linkRows, "SELECT COUNT(*) FROM conversation_messages"); err != nil {
		t.Fatalf("count conversation_messages: %v", err)
	}
	if msgRows != 0 || convRows != 0 || linkRows != 0 {
		t.Fatalf("expected pruned rows, got messages=%d conversations=%d links=%d", msgRows, convRows, linkRows)
	}
}

// TestSQLitePersistsTerminalAt asserts terminal_at round-trips so terminal
// retention is measured from the actual terminal flip across restarts.
func TestSQLitePersistsTerminalAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  func() time.Time { return current },
	}

	s, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	for _, id := range []string{"ucla.a", "ucla.b"} {
		if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: id, Mode: AgentModePull, TTLSeconds: 60}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	// Inform messages are terminal at creation.
	msg, _, err := s.SendMessage(SendMessageInput{
		To: "ucla.b", From: "ucla.a", RequestID: "r1", Type: MessageTypeInform, Body: "done",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen sqlite store: %v", err)
	}
	defer s2.Close()
	got, ok := s2.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("message missing after reload")
	}
	if !got.TerminalAt.Equal(current) {
		t.Fatalf("expected terminal_at %v after reload, got %v", current, got.TerminalAt)
	}
}
