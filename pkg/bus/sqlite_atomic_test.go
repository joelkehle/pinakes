package bus

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestSQLitePersistAfterSendIsAtomic is the regression for the conversation-
// count drift observed when bouncing v0.2.1 buses: SendMessage's SQLite
// persistence used four separate auto-commit Execs (conversation, message,
// conversation_messages, counters). A crash mid-persist could leave a
// counter advanced past an unwritten row, or a conversation without its
// message link. v0.2.2 wraps all four writes in a single transaction. This
// test injects a failure between the last write and Commit; afterward it
// reopens the store and asserts that none of the four rows leaked through.
func TestSQLitePersistAfterSendIsAtomic(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "atomic.db")
	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  func() time.Time { return now },
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, TTLSeconds: 60}); err != nil {
		t.Fatalf("register b: %v", err)
	}

	// Force a failure inside persistAfterSend's transaction. The hook fires
	// after all four save* writes but before Commit, mirroring a crash that
	// would have left partial rows behind under the v0.2.1 implementation.
	injected := errors.New("injected persist failure")
	s1.testHookBeforeCommit = func() error { return injected }

	_, _, err = s1.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-rollback", Type: MessageTypeRequest, Body: "should not persist",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected error, got %v", err)
	}

	// In-memory state did advance for the failed send (counter, message
	// map) — that is the SendMessage contract. The atomic guarantee is
	// only about what makes it to disk. So drop the hook and verify the
	// next send succeeds at m-000002: the in-memory counter advanced
	// through the rolled-back attempt, so on disk we will see m-000002
	// with no m-000001 row — that gap is the rollback working correctly.
	s1.testHookBeforeCommit = nil
	good, _, err := s1.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-good", Type: MessageTypeRequest, Body: "should persist",
	})
	if err != nil {
		t.Fatalf("send after rollback: %v", err)
	}
	if good.MessageID != "m-000002" {
		t.Fatalf("expected counter to keep advancing through failed sends, got %s", good.MessageID)
	}
	goodID := good.MessageID
	goodConvID := good.ConversationID

	s1.Close()

	// Reopen from disk. The rolled-back message must not be present, and
	// the successful message must be.
	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.inner.messages["m-000001"]; ok {
		t.Fatalf("rolled-back message m-000001 leaked to disk")
	}
	if _, ok := s2.inner.messages[goodID]; !ok {
		t.Fatalf("committed message %s missing after reopen", goodID)
	}

	// nextMessageID on disk should be 2 — the counter for the committed
	// send. If the rolled-back persist had written its counter alone, we'd
	// see 1 here.
	if s2.inner.nextMessageID != 2 {
		t.Fatalf("expected nextMessageID=2 after reopen, got %d", s2.inner.nextMessageID)
	}

	// And exactly one conversation_messages row must exist for the surviving
	// conversation — the one tied to m-000002. The rolled-back send's
	// position link must not be there.
	rows, err := s2.db.Query("SELECT conversation_id, message_id, position FROM conversation_messages WHERE conversation_id = ?", goodConvID)
	if err != nil {
		t.Fatalf("query conversation_messages: %v", err)
	}
	defer rows.Close()
	var linkCount int
	for rows.Next() {
		var cid, mid string
		var pos int
		if err := rows.Scan(&cid, &mid, &pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if mid != goodID {
			t.Fatalf("unexpected conversation_messages row mid=%s pos=%d", mid, pos)
		}
		linkCount++
	}
	if linkCount != 1 {
		t.Fatalf("expected exactly 1 conversation_messages row for conv %s, got %d", goodConvID, linkCount)
	}
}

// TestSQLiteCreateConversationIsAtomic is the matching guard for the
// CreateConversation tx: conversation row and counters move together or
// not at all.
func TestSQLiteCreateConversationIsAtomic(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "atomic_create.db")
	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  func() time.Time { return now },
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	injected := errors.New("injected create failure")
	s1.testHookBeforeCommit = func() error { return injected }

	if _, err := s1.CreateConversation(CreateConversationInput{Title: "rolled back"}); !errors.Is(err, injected) {
		t.Fatalf("expected injected error, got %v", err)
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if got := len(s2.ListConversations(ListConversationsFilter{})); got != 0 {
		t.Fatalf("expected 0 conversations after rollback, got %d", got)
	}
}
