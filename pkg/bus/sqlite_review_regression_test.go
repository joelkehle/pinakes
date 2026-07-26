package bus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteConcurrentAcceptanceAndStateChangesDoNotDeadlock(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "lock-order.db"), Config{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registerDeliveryPair(t, s)

	const eventCount = 20
	requests := make([]*Message, 0, eventCount)
	for i := 0; i < eventCount; i++ {
		message, _, err := s.SendMessage(SendMessageInput{
			From: "ucla.sender", To: "ucla.receiver",
			RequestID: fmt.Sprintf("event-%d", i),
			Type:      MessageTypeRequest,
			Body:      "work",
		})
		if err != nil {
			t.Fatalf("seed request %d: %v", i, err)
		}
		requests = append(requests, message)
	}

	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _, err := s.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: fmt.Sprintf("send-%d", i),
				Type:      MessageTypeInform,
				Body:      "concurrent send",
			})
			if err != nil {
				errs <- fmt.Errorf("send %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := s.Inject(InjectInput{
				Identity: "review-regression",
				To:       "ucla.receiver",
				Body:     fmt.Sprintf("injection %d", i),
			}); err != nil {
				errs <- fmt.Errorf("inject %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i, message := range requests {
			if err := s.PostEvent(EventInput{
				ActorAgentID: "ucla.receiver",
				MessageID:    message.MessageID,
				Type:         "final",
				Body:         fmt.Sprintf("result %d", i),
			}); err != nil {
				errs <- fmt.Errorf("event %d: %w", i, err)
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent SQLite operations deadlocked")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func TestSQLiteQueuedDeliveryPersistsCanonicalWaitingState(t *testing.T) {
	for _, mode := range []AgentMode{AgentModePull, AgentModePush} {
		t.Run(string(mode), func(t *testing.T) {
			callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer callback.Close()

			var clockNanos atomic.Int64
			clockNanos.Store(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).UnixNano())
			cfg := Config{
				Clock:           func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() },
				PushMaxAttempts: 1,
			}
			dbPath := filepath.Join(t.TempDir(), "waiting-state.db")
			s, err := NewSQLiteStore(dbPath, cfg)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			if _, err := s.RegisterAgent(RegisterAgentInput{
				AgentID: "ucla.sender", Mode: AgentModePull, TTLSeconds: 3600,
			}); err != nil {
				t.Fatalf("register sender: %v", err)
			}
			register := RegisterAgentInput{
				AgentID: "ucla.receiver", Mode: mode, TTLSeconds: 1,
			}
			if mode == AgentModePush {
				register.CallbackURL = callback.URL
			}
			if _, err := s.RegisterAgent(register); err != nil {
				t.Fatalf("register receiver: %v", err)
			}
			clockNanos.Add((2 * time.Second).Nanoseconds())
			message, _, err := s.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: "queued-waiting-state",
				Type:      MessageTypeRequest,
				Body:      "resume after registration",
			})
			if err != nil {
				t.Fatalf("queue message: %v", err)
			}
			register.TTLSeconds = 3600
			if _, err := s.RegisterAgent(register); err != nil {
				t.Fatalf("re-register receiver: %v", err)
			}
			if mode == AgentModePull {
				events, _, err := s.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
				if err != nil || len(events) != 1 {
					t.Fatalf("poll queued message: events=%d err=%v", len(events), err)
				}
			} else {
				waitForDeliveryStatus(t, s, message.MessageID, "received", 3*time.Second)
			}

			var storedState string
			if err := s.db.Get(&storedState, `SELECT state FROM messages WHERE message_id = ?`, message.MessageID); err != nil {
				t.Fatalf("read stored state: %v", err)
			}
			if storedState != string(StateWaitingAck) {
				t.Fatalf("stored state=%q want=%q", storedState, StateWaitingAck)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			reopened, err := NewSQLiteStore(dbPath, cfg)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			defer reopened.Close()
			restored, ok := reopened.GetMessageForTest(message.MessageID)
			if !ok || restored.State != StateWaitingAck {
				t.Fatalf("restored message=%+v found=%v", restored, ok)
			}
		})
	}
}

func TestSQLiteEventWithoutAckRecordsTransportReceipt(t *testing.T) {
	tests := []struct {
		eventType string
		wantState MessageState
	}{
		{eventType: "progress", wantState: StateExecuting},
		{eventType: "final", wantState: StateCompleted},
		{eventType: "error", wantState: StateError},
	}
	for _, test := range tests {
		t.Run(test.eventType, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "event-receipt.db")
			s, err := NewSQLiteStore(dbPath, Config{})
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			registerDeliveryPair(t, s)
			message, _, err := s.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: "event-without-ack",
				Type:      MessageTypeRequest,
				Body:      "work",
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			events, _, err := s.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
			if err != nil || len(events) != 1 {
				t.Fatalf("offer message: events=%d err=%v", len(events), err)
			}
			if err := s.PostEvent(EventInput{
				ActorAgentID: "ucla.receiver",
				MessageID:    message.MessageID,
				Type:         test.eventType,
				Body:         "recipient result",
			}); err != nil {
				t.Fatalf("post event: %v", err)
			}
			var receiptStatus string
			if err := s.db.Get(&receiptStatus, `SELECT status FROM deliveries WHERE message_id = ?`, message.MessageID); err != nil {
				t.Fatalf("read event receipt: %v", err)
			}
			if receiptStatus != "received" {
				t.Fatalf("event receipt status=%q want=received", receiptStatus)
			}
			waitForDeliveryStatus(t, s, message.MessageID, "received", time.Second)
			if err := s.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			reopened, err := NewSQLiteStore(dbPath, Config{})
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			defer reopened.Close()
			events, _, err = reopened.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
			if err != nil {
				t.Fatalf("poll after restart: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("received request replayed after %s: %#v", test.eventType, events)
			}
			restored, ok := reopened.GetMessageForTest(message.MessageID)
			if !ok || restored.State != test.wantState {
				t.Fatalf("restored message=%+v found=%v want state=%s", restored, ok, test.wantState)
			}
		})
	}
}

func TestSQLitePushInjectionRecordsInitialReceipt(t *testing.T) {
	var callbacks atomic.Int64
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbacks.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	dbPath := filepath.Join(t.TempDir(), "push-inject.db")
	s, err := NewSQLiteStore(dbPath, Config{PushMaxAttempts: 1})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	message, err := s.Inject(InjectInput{
		Identity: "review-regression",
		To:       "ucla.receiver",
		Body:     "deliver exactly once",
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	waitForDeliveryStatus(t, s, message.MessageID, "received", 3*time.Second)
	time.Sleep(350 * time.Millisecond)
	if got := callbacks.Load(); got != 1 {
		t.Fatalf("push injection callbacks=%d want=1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath, Config{PushMaxAttempts: 1})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	if got := callbacks.Load(); got != 1 {
		t.Fatalf("received injection replayed after restart: callbacks=%d", got)
	}
}

func TestSQLiteQueuedDeliveryExpiresAtGraceDeadline(t *testing.T) {
	var clockNanos atomic.Int64
	start := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	clockNanos.Store(start.UnixNano())
	cfg := Config{
		Clock:       func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() },
		GracePeriod: 3 * time.Second,
	}
	dbPath := filepath.Join(t.TempDir(), "grace-expiry.db")
	s, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.sender", Mode: AgentModePull, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePull, TTLSeconds: 1,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	clockNanos.Add((2 * time.Second).Nanoseconds())
	message, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver",
		RequestID:  "grace-deadline",
		Type:       MessageTypeRequest,
		Body:       "do not deliver after grace",
		TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("queue message: %v", err)
	}
	var storedExpiry string
	if err := s.db.Get(&storedExpiry, `SELECT expires_at FROM deliveries WHERE message_id = ?`, message.MessageID); err != nil {
		t.Fatalf("read delivery expiry: %v", err)
	}
	expiresAt, err := parseSQLiteTime(storedExpiry)
	if err != nil {
		t.Fatalf("parse delivery expiry: %v", err)
	}
	wantExpiry := start.Add(4 * time.Second)
	if !expiresAt.Equal(wantExpiry) {
		t.Fatalf("delivery expiry=%s want grace deadline=%s", expiresAt, wantExpiry)
	}

	clockNanos.Store(start.Add(5 * time.Second).UnixNano())
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePull, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("re-register receiver: %v", err)
	}
	if err := s.pruneDB(start.Add(5 * time.Second)); err != nil {
		t.Fatalf("expire delivery: %v", err)
	}
	events, _, err := s.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil {
		t.Fatalf("poll after grace: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("message delivered after grace: %#v", events)
	}
	waitForDeliveryStatus(t, s, message.MessageID, "failed", time.Second)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	events, _, err = reopened.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil {
		t.Fatalf("poll after restart: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expired delivery replayed after restart: %#v", events)
	}
}

func TestSQLiteDurableInboxReturnsBoundedBatches(t *testing.T) {
	t.Run("event count", func(t *testing.T) {
		s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-limit.db"), Config{
			MaxInboxEventsPerAgent: 2,
			MaxInboxBytesPerAgent:  1 << 20,
		})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer s.Close()
		registerDeliveryPair(t, s)
		for i := 0; i < 5; i++ {
			if _, _, err := s.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: fmt.Sprintf("batch-%d", i),
				Type:      MessageTypeInform,
				Body:      fmt.Sprintf("message %d", i),
			}); err != nil {
				t.Fatalf("send %d: %v", i, err)
			}
		}
		cursor := 0
		for batch, want := range []int{2, 2, 1} {
			events, next, err := s.PollInbox(PollInboxInput{
				AgentID: "ucla.receiver",
				Cursor:  cursor,
			})
			if err != nil {
				t.Fatalf("poll batch %d: %v", batch, err)
			}
			if len(events) != want || next != cursor+want {
				t.Fatalf("batch %d events=%d next=%d want events=%d next=%d",
					batch, len(events), next, want, cursor+want)
			}
			cursor = next
		}
	})

	t.Run("byte count", func(t *testing.T) {
		s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "byte-limit.db"), Config{
			MaxInboxEventsPerAgent: 10,
			MaxInboxBytesPerAgent:  250,
		})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer s.Close()
		registerDeliveryPair(t, s)
		for i := 0; i < 3; i++ {
			if _, _, err := s.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: fmt.Sprintf("bytes-%d", i),
				Type:      MessageTypeInform,
				Body:      strings.Repeat("x", 120),
			}); err != nil {
				t.Fatalf("send %d: %v", i, err)
			}
		}
		events, next, err := s.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if len(events) != 1 || next != 1 {
			t.Fatalf("byte-bounded events=%d next=%d want events=1 next=1", len(events), next)
		}
	})
}
