package bus

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newRetentionStore(clock func() time.Time) *Store {
	return NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      48 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		MessageRetention:       time.Hour,
		MessageMaxAge:          12 * time.Hour,
		ConversationRetention:  2 * time.Hour,
		AgentRetention:         3 * time.Hour,
		Clock:                  clock,
	})
}

func registerRetentionPair(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"ucla.a", "ucla.b"} {
		if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: id, Mode: AgentModePull, TTLSeconds: 60}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}

// TestSweepPrunesTerminalMessages is the regression guard for the 2026-06-09/10
// OOM kills: terminal messages (and eventually their conversations) must be
// reclaimed so bus memory stays bounded between restarts.
func TestSweepPrunesTerminalMessages(t *testing.T) {
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newRetentionStore(func() time.Time { return current })
	registerRetentionPair(t, s)

	msg, _, err := s.SendMessage(SendMessageInput{
		To: "ucla.b", From: "ucla.a", RequestID: "r1", Type: MessageTypeRequest, Body: "work", TTLSeconds: 1,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// TTL expiry flips the message terminal and stamps TerminalAt.
	current = current.Add(5 * time.Second)
	_ = s.ListAgents("")
	got, ok := s.GetMessageForTest(msg.MessageID)
	if !ok || got.State != StateError {
		t.Fatalf("expected error state after ttl, got ok=%v state=%s", ok, got.State)
	}
	if got.TerminalAt.IsZero() {
		t.Fatalf("expected TerminalAt to be stamped on terminal flip")
	}

	// Within retention: still present.
	current = current.Add(30 * time.Minute)
	_ = s.ListAgents("")
	if _, ok := s.GetMessageForTest(msg.MessageID); !ok {
		t.Fatalf("message pruned before retention elapsed")
	}

	// Past retention: pruned.
	current = current.Add(time.Hour)
	_ = s.ListAgents("")
	if _, ok := s.GetMessageForTest(msg.MessageID); ok {
		t.Fatalf("terminal message not pruned after retention")
	}

	// Past conversation retention with no live messages: conversation and its
	// message-id index pruned too.
	current = current.Add(2 * time.Hour)
	_ = s.ListAgents("")
	s.mu.Lock()
	convCount := len(s.conversations)
	idxCount := len(s.conversationMessages)
	s.mu.Unlock()
	if convCount != 0 || idxCount != 0 {
		t.Fatalf("expected conversations pruned, got conversations=%d index=%d", convCount, idxCount)
	}

	// Past agent retention: expired agents and their inboxes pruned.
	current = current.Add(3 * time.Hour)
	_ = s.ListAgents("")
	s.mu.Lock()
	agentCount := len(s.agents)
	inboxCount := len(s.inboxes)
	s.mu.Unlock()
	if agentCount != 0 || inboxCount != 0 {
		t.Fatalf("expected expired agents pruned, got agents=%d inboxes=%d", agentCount, inboxCount)
	}
}

// TestSweepPrunesByMaxAgeBackstop covers legacy/stuck messages: anything older
// than MessageMaxAge is pruned regardless of state, including messages whose
// TTL deadline was lost (zero) by pre-fix persistence.
func TestSweepPrunesByMaxAgeBackstop(t *testing.T) {
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newRetentionStore(func() time.Time { return current })

	s.mu.Lock()
	s.messages["m-legacy"] = &Message{
		MessageID: "m-legacy",
		Type:      MessageTypeRequest,
		From:      "ucla.a",
		To:        "ucla.b",
		Body:      "stuck",
		State:     StateWaitingAck,
		CreatedAt: current,
		// TTLExpiresAt zero: simulates state loaded from a pre-fix state file.
	}
	s.mu.Unlock()

	current = current.Add(13 * time.Hour)
	_ = s.ListAgents("")
	if _, ok := s.GetMessageForTest("m-legacy"); ok {
		t.Fatalf("expected max-age backstop to prune stuck message")
	}
}

// TestPollInboxReclaimsDeliveredEvents asserts that polling at cursor C frees
// the events below C instead of holding them until a cap evicts them.
func TestPollInboxReclaimsDeliveredEvents(t *testing.T) {
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newRetentionStore(func() time.Time { return current })
	registerRetentionPair(t, s)

	for i := 0; i < 2; i++ {
		if _, _, err := s.SendMessage(SendMessageInput{
			To: "ucla.b", From: "ucla.a", RequestID: fmt.Sprintf("r%d", i), Type: MessageTypeRequest, Body: "x",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	events, next, err := s.PollInbox(PollInboxInput{AgentID: "ucla.b", Cursor: 0})
	if err != nil || len(events) != 2 || next != 2 {
		t.Fatalf("first poll: events=%d next=%d err=%v", len(events), next, err)
	}

	// Second poll at the advanced cursor proves receipt; events reclaimed.
	if _, _, err := s.PollInbox(PollInboxInput{AgentID: "ucla.b", Cursor: next}); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	s.mu.Lock()
	inboxLen := len(s.inboxes["ucla.b"])
	base := s.inboxBase["ucla.b"]
	bytes := s.inboxBytes["ucla.b"]
	s.mu.Unlock()
	if inboxLen != 0 || base != 2 || bytes != 0 {
		t.Fatalf("expected reclaimed inbox, got len=%d base=%d bytes=%d", inboxLen, base, bytes)
	}
}

// TestInboxByteBudgetEvictsOldest asserts large payloads are bounded by bytes,
// not just event count, and that the newest event is always kept.
func TestInboxByteBudgetEvictsOldest(t *testing.T) {
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
		MaxInboxBytesPerAgent: 300,
		Clock:                 func() time.Time { return current },
	})
	registerRetentionPair(t, s)

	body := strings.Repeat("x", 200) // each event > 264 bytes with overhead
	for i := 0; i < 3; i++ {
		if _, _, err := s.SendMessage(SendMessageInput{
			To: "ucla.b", From: "ucla.a", RequestID: fmt.Sprintf("r%d", i), Type: MessageTypeRequest, Body: body,
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	s.mu.Lock()
	inboxLen := len(s.inboxes["ucla.b"])
	base := s.inboxBase["ucla.b"]
	var last InboxEvent
	if inboxLen > 0 {
		last = s.inboxes["ucla.b"][inboxLen-1]
	}
	s.mu.Unlock()
	if inboxLen != 1 || base != 2 {
		t.Fatalf("expected byte budget to keep only newest event, got len=%d base=%d", inboxLen, base)
	}
	if last.MessageID == "" {
		t.Fatalf("expected newest event retained")
	}
}

// TestObserveByteBudgetEvictsOldest asserts the observe ring is bounded by
// payload bytes, keeping at least the newest event.
func TestObserveByteBudgetEvictsOldest(t *testing.T) {
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
		MaxObserveBytes: 1,
		Clock:           func() time.Time { return current },
	})

	for i := 0; i < 3; i++ {
		if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: fmt.Sprintf("ucla.agent-%d", i), Mode: AgentModePull, TTLSeconds: 60}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	s.mu.Lock()
	count := len(s.observeEvents)
	bytes := s.observeBytes
	var lastID int64
	if count > 0 {
		lastID = s.observeEvents[count-1].ID
	}
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected observe byte budget to keep only newest event, got %d", count)
	}
	if lastID != 3 {
		t.Fatalf("expected newest observe event retained, got id=%d", lastID)
	}
	if bytes <= 0 {
		t.Fatalf("expected positive byte accounting, got %d", bytes)
	}
}
