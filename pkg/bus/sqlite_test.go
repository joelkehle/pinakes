package bus

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "roundtrip.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}

	// Open, write data, close.
	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}

	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	msg, _, err := s1.SendMessage(SendMessageInput{
		To:        "b",
		From:      "a",
		RequestID: "rid-sqlite-1",
		Type:      MessageTypeRequest,
		Body:      "persist in sqlite",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	s1.Close()

	// Reopen and verify data survived.
	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen sqlite store: %v", err)
	}
	defer s2.Close()

	agents := s2.ListAgents("")
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after restore, got %d", len(agents))
	}

	// Messages should be loadable from the conversation.
	_, msgs, _, err := s2.ListConversationMessages(ListConversationMessagesInput{
		ConversationID: msg.ConversationID,
		Cursor:         0,
		Limit:          50,
	})
	if err != nil {
		t.Fatalf("list messages after restore: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after restore, got %d", len(msgs))
	}
	if msgs[0].MessageID != msg.MessageID {
		t.Fatalf("expected message %s, got %s", msg.MessageID, msgs[0].MessageID)
	}
	if msgs[0].Body != "persist in sqlite" {
		t.Fatalf("expected body 'persist in sqlite', got %q", msgs[0].Body)
	}
}

func TestSQLiteAckPersists(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "ack.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	msg, _, err := s1.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-ack", Type: MessageTypeRequest, Body: "ack me",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := s1.Ack(AckInput{AgentID: "b", MessageID: msg.MessageID, Status: "accepted"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	restored, ok := s2.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("message %s missing after reopen", msg.MessageID)
	}
	if restored.State != StateExecuting {
		t.Fatalf("expected executing state after ack persist, got %s", restored.State)
	}
}

func TestSQLiteEventFinalPersists(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "final.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	msg, _, err := s1.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-final", Type: MessageTypeRequest, Body: "complete me",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := s1.Ack(AckInput{AgentID: "b", MessageID: msg.MessageID, Status: "accepted"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := s1.PostEvent(EventInput{ActorAgentID: "b", MessageID: msg.MessageID, Type: "final", Body: "done"}); err != nil {
		t.Fatalf("final event: %v", err)
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	restored, ok := s2.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("message %s missing after reopen", msg.MessageID)
	}
	if restored.State != StateCompleted {
		t.Fatalf("expected completed state after final event persist, got %s", restored.State)
	}
}

func TestSQLiteConversationPersists(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "conv.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	conv, err := s1.CreateConversation(CreateConversationInput{
		Title:        "test conv",
		Participants: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	convs := s2.ListConversations(ListConversationsFilter{})
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation after restore, got %d", len(convs))
	}
	if convs[0].ConversationID != conv.ConversationID {
		t.Fatalf("conversation id mismatch: %s vs %s", convs[0].ConversationID, conv.ConversationID)
	}
	if convs[0].Title != "test conv" {
		t.Fatalf("expected title 'test conv', got %q", convs[0].Title)
	}
}

func TestSQLiteCountersPreserved(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "counters.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
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
	// Send 3 messages to advance the counter.
	for i := 1; i <= 3; i++ {
		_, _, err := s1.SendMessage(SendMessageInput{
			To: "b", From: "a", RequestID: "rid-cnt-" + string(rune('0'+i)), Type: MessageTypeRequest, Body: "msg",
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// Register agents again (they expired on reload without inbox).
	if _, err := s2.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, TTLSeconds: 60}); err != nil {
		t.Fatalf("re-register a: %v", err)
	}
	if _, err := s2.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, TTLSeconds: 60}); err != nil {
		t.Fatalf("re-register b: %v", err)
	}

	// Next message should be m-000004, not m-000001.
	msg, _, err := s2.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-cnt-after", Type: MessageTypeRequest, Body: "after restart",
	})
	if err != nil {
		t.Fatalf("send after reopen: %v", err)
	}
	if msg.MessageID != "m-000004" {
		t.Fatalf("expected m-000004 after counter restore, got %s", msg.MessageID)
	}
}

func TestSQLiteInjectPersists(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "inject.db")
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	msg, err := s1.Inject(InjectInput{
		Identity: "tester",
		To:       "a",
		Body:     "human says hi",
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	s1.Close()

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	restored, ok := s2.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("injected message missing after reopen")
	}
	if restored.Body != "human says hi" {
		t.Fatalf("expected body 'human says hi', got %q", restored.Body)
	}
}
