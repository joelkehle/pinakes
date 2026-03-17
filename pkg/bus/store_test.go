package bus

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	store := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           2 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		PushMaxAttempts:        3,
		PushBaseBackoff:        10 * time.Millisecond,
		Clock: func() time.Time {
			return now
		},
	})
	return store, &now
}

func registerPair(t *testing.T, s *Store, ttlA, ttlB int) {
	t.Helper()
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: ttlA}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: ttlB}); err != nil {
		t.Fatalf("register b: %v", err)
	}
}

func TestSendMessageIdempotency(t *testing.T) {
	s, _ := newTestStore(t)
	registerPair(t, s, 60, 60)

	m1, dup1, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-1", Type: MessageTypeRequest, Body: "hello",
	})
	if err != nil {
		t.Fatalf("send1: %v", err)
	}
	if dup1 {
		t.Fatalf("expected first send to be non-duplicate")
	}

	m2, dup2, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-1", Type: MessageTypeRequest, Body: "hello again",
	})
	if err != nil {
		t.Fatalf("send2: %v", err)
	}
	if !dup2 {
		t.Fatalf("expected second send to be duplicate")
	}
	if m1.MessageID != m2.MessageID {
		t.Fatalf("expected same message_id, got %s vs %s", m1.MessageID, m2.MessageID)
	}

	events, _, err := s.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
	if err != nil {
		t.Fatalf("poll inbox: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 inbox event, got %d", len(events))
	}
}

func TestEventOwnershipEnforced(t *testing.T) {
	s, _ := newTestStore(t)
	registerPair(t, s, 60, 60)

	msg, _, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-2", Type: MessageTypeRequest, Body: "work",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	err = s.PostEvent(EventInput{
		ActorAgentID: "a",
		MessageID:    msg.MessageID,
		Type:         "progress",
		Body:         "doing",
	})
	if err == nil {
		t.Fatalf("expected unauthorized error")
	}
	be, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if be.Code != CodeUnauthorized {
		t.Fatalf("expected code unauthorized, got %s", be.Code)
	}
}

func TestProgressRateLimit(t *testing.T) {
	s, now := newTestStore(t)
	registerPair(t, s, 60, 60)

	msg, _, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-3", Type: MessageTypeRequest, Body: "work",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := s.Ack(AckInput{AgentID: "b", MessageID: msg.MessageID, Status: "accepted"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := s.PostEvent(EventInput{ActorAgentID: "b", MessageID: msg.MessageID, Type: "progress", Body: "10%"}); err != nil {
		t.Fatalf("progress1: %v", err)
	}
	if err := s.PostEvent(EventInput{ActorAgentID: "b", MessageID: msg.MessageID, Type: "progress", Body: "20%"}); err == nil {
		t.Fatalf("expected rate limit error")
	} else {
		be, ok := err.(*Error)
		if !ok || be.Code != CodeRateLimited {
			t.Fatalf("expected CodeRateLimited, got %#v", err)
		}
	}

	*now = now.Add(3 * time.Second)
	if err := s.PostEvent(EventInput{ActorAgentID: "b", MessageID: msg.MessageID, Type: "progress", Body: "30%"}); err != nil {
		t.Fatalf("progress after delay: %v", err)
	}
}

func TestExpiredTargetGraceThenError(t *testing.T) {
	s, now := newTestStore(t)
	registerPair(t, s, 60, 1)

	*now = now.Add(2 * time.Second)
	msg, _, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-4", Type: MessageTypeRequest, Body: "queued",
	})
	if err != nil {
		t.Fatalf("send while within grace: %v", err)
	}
	if !msg.QueuedForAgent {
		t.Fatalf("expected message to be queued for expired agent")
	}

	*now = now.Add(40 * time.Second)
	_ = s.Health() // triggers sweep

	stored, ok := s.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("expected message to exist")
	}
	if stored.State != StateError {
		t.Fatalf("expected state error after grace, got %s", stored.State)
	}
}

func TestPushModeRetriesToCallback(t *testing.T) {
	s, _ := newTestStore(t)
	var attempts int32
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()

	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "p", Mode: AgentModePush, CallbackURL: callback.URL, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register p: %v", err)
	}
	if _, _, err := s.SendMessage(SendMessageInput{
		To:        "p",
		From:      "a",
		RequestID: "rid-push",
		Type:      MessageTypeRequest,
		Body:      "hello push",
	}); err != nil {
		t.Fatalf("send push message: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&attempts) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Fatalf("expected at least 3 callback attempts, got %d", got)
	}
}

func TestConcurrentIdempotentSendSingleDelivery(t *testing.T) {
	s, _ := newTestStore(t)
	registerPair(t, s, 60, 60)

	const workers = 32
	type result struct {
		id  string
		dup bool
		err error
	}
	out := make(chan result, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			m, dup, err := s.SendMessage(SendMessageInput{
				To: "b", From: "a", RequestID: "rid-concurrent", Type: MessageTypeRequest, Body: "hello",
			})
			if err != nil {
				out <- result{err: err}
				return
			}
			out <- result{id: m.MessageID, dup: dup}
		}()
	}
	wg.Wait()
	close(out)

	ids := map[string]int{}
	nonDup := 0
	for r := range out {
		if r.err != nil {
			t.Fatalf("send error: %v", r.err)
		}
		ids[r.id]++
		if !r.dup {
			nonDup++
		}
	}
	if len(ids) != 1 {
		t.Fatalf("expected one message id, got %#v", ids)
	}
	if nonDup != 1 {
		t.Fatalf("expected exactly one non-duplicate send, got %d", nonDup)
	}

	events, _, err := s.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
	if err != nil {
		t.Fatalf("poll inbox: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one inbox event, got %d", len(events))
	}
}

func TestInboxCursorAfterTrim(t *testing.T) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		MaxInboxEventsPerAgent: 3,
		Clock: func() time.Time {
			return now
		},
	})
	registerPair(t, s, 60, 60)

	for i := 1; i <= 5; i++ {
		_, _, err := s.SendMessage(SendMessageInput{
			To: "b", From: "a", RequestID: "rid-trim-" + strconv.Itoa(i), Type: MessageTypeRequest, Body: "msg",
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	events, next, err := s.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
	if err != nil {
		t.Fatalf("poll inbox: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 retained events, got %d", len(events))
	}
	if next != 5 {
		t.Fatalf("expected next cursor 5, got %d", next)
	}
	if events[0].MessageID != "m-000003" {
		t.Fatalf("expected first retained message m-000003, got %s", events[0].MessageID)
	}
}

func TestAckTimeoutTransitionsToErrorOnSweep(t *testing.T) {
	s, now := newTestStore(t)
	registerPair(t, s, 60, 60)

	msg, _, err := s.SendMessage(SendMessageInput{
		To: "b", From: "a", RequestID: "rid-ack-timeout", Type: MessageTypeRequest, Body: "hello",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	*now = now.Add(11 * time.Second)
	_ = s.Health() // triggers sweep

	stored, ok := s.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("message not found after timeout sweep")
	}
	if stored.State != StateError {
		t.Fatalf("expected error state, got %s", stored.State)
	}
}
