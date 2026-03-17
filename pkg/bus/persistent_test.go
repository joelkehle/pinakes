package bus

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentStoreRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
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

	s1, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("new persistent store: %v", err)
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
		RequestID: "rid-persist",
		Type:      MessageTypeRequest,
		Body:      "persist me",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	s2, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("re-open persistent store: %v", err)
	}
	agents := s2.ListAgents("")
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after restore, got %d", len(agents))
	}
	events, _, err := s2.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
	if err != nil {
		t.Fatalf("poll inbox after restore: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 inbox event after restore, got %d", len(events))
	}
	if events[0].MessageID != msg.MessageID {
		t.Fatalf("expected message %s, got %s", msg.MessageID, events[0].MessageID)
	}
}

func TestPersistentStoreReadSweepPersist(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	base := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	current := base
	clock := func() time.Time {
		return current
	}
	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  clock,
	}

	store, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("new persistent store for read sweep test: %v", err)
	}
	if _, err := store.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register agent a: %v", err)
	}
	if _, err := store.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register agent b: %v", err)
	}
	msg, _, err := store.SendMessage(SendMessageInput{
		To:         "b",
		From:       "a",
		RequestID:  "rid-sweep",
		Type:       MessageTypeRequest,
		Body:       "expire me",
		TTLSeconds: 1,
	})
	if err != nil {
		t.Fatalf("send message for read sweep test: %v", err)
	}

	current = current.Add(5 * time.Second)
	_ = store.ListAgents("")

	restored, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("reopen persistent store for read sweep test: %v", err)
	}
	restoredMsg, ok := restored.inner.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("message %s missing after reopen", msg.MessageID)
	}
	if restoredMsg.State != StateError {
		t.Fatalf("expected message in error state after sweep persistence, got %s", restoredMsg.State)
	}
}

func TestPersistentStoreRejectsCorruptStateFile(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	if err := os.WriteFile(statePath, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write corrupt state file: %v", err)
	}

	cfg := Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  time.Now,
	}
	if _, err := NewPersistentStore(statePath, cfg); err == nil {
		t.Fatalf("expected error when opening corrupt state file")
	}
}
