package bus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func registerDeliveryPair(t *testing.T, s *SQLiteStore) {
	t.Helper()
	for _, id := range []string{"ucla.sender", "ucla.receiver"} {
		if _, err := s.RegisterAgent(RegisterAgentInput{
			AgentID: id, Mode: AgentModePull, TTLSeconds: 3600,
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}

func TestSQLitePendingDeliverySurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "delivery.db")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{Clock: func() time.Time { return now }}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registerDeliveryPair(t, s1)
	msg, _, err := s1.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "restart-1",
		Type: MessageTypeRequest, Body: "survive restart",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	events, next, err := s1.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil || len(events) != 1 || next != 1 {
		t.Fatalf("pre-crash poll events=%d next=%d err=%v", len(events), next, err)
	}
	// No follow-up poll at cursor=1: the response may have been lost before
	// the recipient durably advanced its cursor.
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	events, next, err = s2.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != msg.MessageID || next != 1 {
		t.Fatalf("restored events=%#v next=%d", events, next)
	}
}

func TestSQLitePullCursorSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cursor.db")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{Clock: func() time.Time { return now }}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registerDeliveryPair(t, s1)
	for i, body := range []string{"one", "two"} {
		if _, _, err := s1.SendMessage(SendMessageInput{
			From: "ucla.sender", To: "ucla.receiver",
			RequestID: "cursor-" + body, Type: MessageTypeInform, Body: body,
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	events, next, err := s1.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil || len(events) != 2 || next != 2 {
		t.Fatalf("first poll events=%d next=%d err=%v", len(events), next, err)
	}
	events, _, err = s1.PollInbox(PollInboxInput{AgentID: "ucla.receiver", Cursor: next})
	if err != nil || len(events) != 0 {
		t.Fatalf("receipt poll events=%d err=%v", len(events), err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	events, cursor, err := s2.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil || len(events) != 0 || cursor != next {
		t.Fatalf("restored receipt events=%d cursor=%d err=%v", len(events), cursor, err)
	}
}

func TestSQLiteDuplicateReceiptOutlivesMessageRetention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dedupe.db")
	current := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{
		Clock:             func() time.Time { return current },
		MessageRetention:  time.Second,
		IdempotencyWindow: 24 * time.Hour,
	}

	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registerDeliveryPair(t, s1)
	original, duplicate, err := s1.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "dedupe-1",
		Type: MessageTypeInform, Body: "one accepted message",
	})
	if err != nil || duplicate {
		t.Fatalf("initial send duplicate=%v err=%v", duplicate, err)
	}
	events, next, err := s1.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil || len(events) != 1 {
		t.Fatalf("poll initial delivery events=%d err=%v", len(events), err)
	}
	if _, _, err := s1.PollInbox(PollInboxInput{
		AgentID: "ucla.receiver", Cursor: next,
	}); err != nil {
		t.Fatalf("record transport receipt: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	current = current.Add(2 * time.Second)
	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	registerDeliveryPair(t, s2)
	retry, duplicate, err := s2.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "dedupe-1",
		Type: MessageTypeInform, Body: "one accepted message",
	})
	if err != nil || !duplicate {
		t.Fatalf("retry duplicate=%v err=%v", duplicate, err)
	}
	if retry.MessageID != original.MessageID {
		t.Fatalf("retry message_id=%s want=%s", retry.MessageID, original.MessageID)
	}
	var rows int
	if err := s2.db.Get(&rows, `SELECT COUNT(*) FROM messages WHERE message_id = ?`, original.MessageID); err != nil {
		t.Fatalf("count retained message: %v", err)
	}
	if rows != 0 {
		t.Fatalf("terminal message should have been pruned")
	}
}

func TestSQLiteUnreceivedResponseAndInformSurviveRestart(t *testing.T) {
	for _, messageType := range []MessageType{MessageTypeResponse, MessageTypeInform} {
		t.Run(string(messageType), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "terminal-delivery.db")
			current := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
			cfg := Config{
				Clock: func() time.Time { return current }, MessageRetention: time.Second,
			}
			s1, err := NewSQLiteStore(dbPath, cfg)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			registerDeliveryPair(t, s1)
			msg, _, err := s1.SendMessage(SendMessageInput{
				From: "ucla.sender", To: "ucla.receiver",
				RequestID: "terminal-" + string(messageType),
				Type:      messageType, Body: "deliver despite terminal lifecycle",
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			_ = s1.Close()
			current = current.Add(2 * time.Second)

			s2, err := NewSQLiteStore(dbPath, cfg)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer s2.Close()
			events, _, err := s2.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
			if err != nil || len(events) != 1 || events[0].MessageID != msg.MessageID {
				t.Fatalf("events=%#v err=%v", events, err)
			}
		})
	}
}

func TestSQLitePushRetrySurvivesRestart(t *testing.T) {
	var succeed atomic.Bool
	var attempts atomic.Int64
	var lastMessageID atomic.Value
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode callback: %v", err)
		}
		lastMessageID.Store(payload["message_id"])
		if !succeed.Load() {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	dbPath := filepath.Join(t.TempDir(), "push.db")
	cfg := Config{
		Clock:           time.Now,
		PushMaxAttempts: 1,
		PushBaseBackoff: 10 * time.Millisecond,
	}
	s1, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.sender", Mode: AgentModePull, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if _, err := s1.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	msg, _, err := s1.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "push-restart",
		Type: MessageTypeInform, Body: "retry me",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForDeliveryStatus(t, s1, msg.MessageID, "pending", 2*time.Second)
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	succeed.Store(true)
	s2, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	waitForDeliveryStatus(t, s2, msg.MessageID, "received", 4*time.Second)
	if got, _ := lastMessageID.Load().(string); got != msg.MessageID {
		t.Fatalf("callback message_id=%q want=%q", got, msg.MessageID)
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected callback before and after restart, attempts=%d", attempts.Load())
	}
	receivedAttempts := attempts.Load()
	if err := s2.Close(); err != nil {
		t.Fatalf("close after receipt: %v", err)
	}
	s3, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen after receipt: %v", err)
	}
	defer s3.Close()
	time.Sleep(1200 * time.Millisecond)
	if got := attempts.Load(); got != receivedAttempts {
		t.Fatalf("durable push receipt replayed callback: attempts=%d want=%d", got, receivedAttempts)
	}
}

func TestSQLiteQueuedPushDeliversAfterReregistration(t *testing.T) {
	delivered := make(chan string, 1)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		delivered <- payload["message_id"].(string)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	var clockNanos atomic.Int64
	clockNanos.Store(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).UnixNano())
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "queued-push.db"), Config{
		Clock:           func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() },
		PushMaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.sender", Mode: AgentModePull, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 1,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	clockNanos.Add((2 * time.Second).Nanoseconds())
	msg, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "queued-push",
		Type: MessageTypeInform, Body: "wait for registration",
	})
	if err != nil {
		t.Fatalf("queue send: %v", err)
	}
	select {
	case <-delivered:
		t.Fatalf("callback ran while target registration was expired")
	case <-time.After(1100 * time.Millisecond):
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("re-register receiver: %v", err)
	}
	select {
	case got := <-delivered:
		if got != msg.MessageID {
			t.Fatalf("callback message_id=%s want=%s", got, msg.MessageID)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("queued push did not resume after re-registration")
	}
}

func waitForDeliveryStatus(t *testing.T, s *SQLiteStore, messageID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var got string
		err := s.db.Get(&got, `SELECT status FROM deliveries WHERE message_id = ?`, messageID)
		if err == nil && got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got string
	_ = s.db.Get(&got, `SELECT status FROM deliveries WHERE message_id = ?`, messageID)
	t.Fatalf("delivery status=%q want=%q", got, want)
}

func TestSQLiteExpiredDeliveryIsVisible(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "expired.db")
	current := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	s, err := NewSQLiteStore(dbPath, Config{Clock: func() time.Time { return current }})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	registerDeliveryPair(t, s)
	msg, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "expire-1",
		Type: MessageTypeInform, Body: "expire visibly", TTLSeconds: 1,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	current = current.Add(2 * time.Second)
	if err := s.pruneDB(current); err != nil {
		t.Fatalf("prune: %v", err)
	}
	waitForDeliveryStatus(t, s, msg.MessageID, "failed", time.Second)
	health := s.Health()
	if got := health["failed_deliveries"]; got != 1 {
		t.Fatalf("failed_deliveries=%v want=1", got)
	}
	if metrics := s.Metrics(); !strings.Contains(metrics, "agent_bus_failed_deliveries 1") {
		t.Fatalf("failed delivery metric missing:\n%s", metrics)
	}
}
