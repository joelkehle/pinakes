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

	"github.com/jmoiron/sqlx"
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

func TestSQLiteLegacyUpgradeBackfillsOnlyUnreceivedDeliveries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-delivery.db")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{Clock: func() time.Time { return now }}

	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatalf("create legacy fixture schema: %v", err)
	}
	for _, id := range []string{"ucla.sender", "ucla.receiver"} {
		if err := saveAgentTo(db, &Agent{
			AgentID: id, Mode: AgentModePull, Status: AgentStatusActive,
			RegisteredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			TTLSeconds: 3600,
		}); err != nil {
			t.Fatalf("save legacy agent %s: %v", id, err)
		}
	}
	conversation := &Conversation{
		ConversationID: "c-legacy", Participants: []string{"ucla.sender", "ucla.receiver"},
		Status: "active", MessageCount: 4,
		CreatedAt: now.Add(-4 * time.Minute), LastMessageAt: now.Add(-time.Minute),
	}
	if err := saveConversationTo(db, conversation); err != nil {
		t.Fatalf("save legacy conversation: %v", err)
	}
	messages := []Message{
		{
			MessageID: "m-pending", Type: MessageTypeRequest,
			From: "ucla.sender", To: "ucla.receiver", ConversationID: "c-legacy",
			RequestID: "legacy-pending", Body: "still waiting", State: StateWaitingAck,
			CreatedAt: now.Add(-4 * time.Minute),
			// A pre-TTL row exercises derivation from the configured default.
		},
		{
			MessageID: "m-executing", Type: MessageTypeRequest,
			From: "ucla.sender", To: "ucla.receiver", ConversationID: "c-legacy",
			RequestID: "legacy-executing", Body: "already accepted", State: StateExecuting,
			CreatedAt: now.Add(-3 * time.Minute), TTLExpiresAt: now.Add(7 * time.Minute),
		},
		{
			MessageID: "m-completed", Type: MessageTypeRequest,
			From: "ucla.sender", To: "ucla.receiver", ConversationID: "c-legacy",
			RequestID: "legacy-completed", Body: "already complete", State: StateCompleted,
			CreatedAt: now.Add(-2 * time.Minute), TerminalAt: now.Add(-time.Minute),
			TTLExpiresAt: now.Add(8 * time.Minute),
		},
		{
			MessageID: "m-inform", Type: MessageTypeInform,
			From: "ucla.sender", To: "ucla.receiver", ConversationID: "c-legacy",
			RequestID: "legacy-inform", Body: "unreceived information", State: StateCompleted,
			CreatedAt: now.Add(-time.Minute), TerminalAt: now.Add(-time.Minute),
			TTLExpiresAt: now.Add(9 * time.Minute),
		},
	}
	for position := range messages {
		if err := saveMessageTo(db, &messages[position]); err != nil {
			t.Fatalf("save legacy message %s: %v", messages[position].MessageID, err)
		}
		if err := saveConversationMessageTo(db, "c-legacy", messages[position].MessageID, position); err != nil {
			t.Fatalf("save legacy conversation link: %v", err)
		}
	}
	if err := saveCountersTo(db, 1, 4); err != nil {
		t.Fatalf("save legacy counters: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy fixture: %v", err)
	}

	s, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("upgrade legacy store: %v", err)
	}
	events, next, err := s.PollInbox(PollInboxInput{AgentID: "ucla.receiver"})
	if err != nil {
		t.Fatalf("poll backfilled inbox: %v", err)
	}
	if len(events) != 2 || next != 2 ||
		events[0].MessageID != "m-pending" || events[1].MessageID != "m-inform" {
		t.Fatalf("backfilled deliveries=%#v next=%d", events, next)
	}
	retry, duplicate, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "legacy-executing",
		Type: MessageTypeRequest, Body: "already accepted",
	})
	if err != nil || !duplicate || retry.MessageID != "m-executing" {
		t.Fatalf("backfilled duplicate receipt=%+v duplicate=%v err=%v", retry, duplicate, err)
	}
	var deliveryRows, markerRows int
	if err := s.db.Get(&deliveryRows, `SELECT COUNT(*) FROM deliveries`); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if err := s.db.Get(&markerRows, `SELECT COUNT(*) FROM schema_migrations WHERE key = ?`,
		durableDeliveryBackfill); err != nil {
		t.Fatalf("count migration marker: %v", err)
	}
	if deliveryRows != 2 || markerRows != 1 {
		t.Fatalf("delivery rows=%d marker rows=%d", deliveryRows, markerRows)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	defer reopened.Close()
	if err := reopened.db.Get(&deliveryRows, `SELECT COUNT(*) FROM deliveries`); err != nil {
		t.Fatalf("count deliveries after reopen: %v", err)
	}
	if deliveryRows != 2 {
		t.Fatalf("backfill reran after marker: delivery rows=%d", deliveryRows)
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

func TestSQLiteCloseDrainsPushReceiptBeforeClosingDatabase(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var attempts atomic.Int64
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	dbPath := filepath.Join(t.TempDir(), "push-drain.db")
	cfg := Config{
		Clock: time.Now, PushMaxAttempts: 1,
		PushShutdownTimeout: 2 * time.Second,
	}
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
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	msg, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "push-drain",
		Type: MessageTypeInform, Body: "persist receipt before close",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback")
	}

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before callback completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("close after callback receipt: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen after drained close: %v", err)
	}
	defer reopened.Close()
	waitForDeliveryStatus(t, reopened, msg.MessageID, "received", time.Second)
	time.Sleep(1200 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("drained callback replayed after restart: attempts=%d", got)
	}
}

func TestSQLiteCloseRecoversAttemptingPushAfterShutdownTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	callback := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	}))
	defer callback.Close()

	dbPath := filepath.Join(t.TempDir(), "push-timeout.db")
	cfg := Config{
		Clock: time.Now, PushMaxAttempts: 1,
		PushShutdownTimeout: 50 * time.Millisecond,
	}
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
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	msg, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "push-timeout",
		Type: MessageTypeInform, Body: "recover interrupted callback",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback")
	}
	if err := s.Close(); err == nil || !strings.Contains(err.Error(), "push shutdown exceeded") {
		t.Fatalf("close error=%v, want explicit shutdown timeout", err)
	}
	close(release)

	db, err := sqlx.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open database after timeout: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.Get(&status, `SELECT status FROM deliveries WHERE message_id = ?`, msg.MessageID); err != nil {
		t.Fatalf("read recovered delivery: %v", err)
	}
	if status != "pending" {
		t.Fatalf("delivery status=%q want=pending", status)
	}
}

func TestSQLiteReceiptErrorSurvivesUnrelatedReceiptSuccess(t *testing.T) {
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	dbPath := filepath.Join(t.TempDir(), "push-receipt-error.db")
	cfg := Config{Clock: time.Now, PushMaxAttempts: 1}
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
		AgentID: "ucla.receiver", Mode: AgentModePush,
		CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_first_receipt
		BEFORE UPDATE OF status ON deliveries
		WHEN OLD.message_id = 'm-000001' AND NEW.status = 'received'
		BEGIN
			SELECT RAISE(ABORT, 'injected receipt persistence failure');
		END`); err != nil {
		t.Fatalf("create receipt failure trigger: %v", err)
	}
	first, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "receipt-error-1",
		Type: MessageTypeInform, Body: "first transport succeeds",
	})
	if err != nil {
		t.Fatalf("send first: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if degraded, _ := s.Health()["delivery_persistence_error"].(bool); degraded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if degraded, _ := s.Health()["delivery_persistence_error"].(bool); !degraded {
		t.Fatal("receipt persistence error did not degrade health")
	}
	waitForDeliveryStatus(t, s, first.MessageID, "attempting", time.Second)

	second, _, err := s.SendMessage(SendMessageInput{
		From: "ucla.sender", To: "ucla.receiver", RequestID: "receipt-error-2",
		Type: MessageTypeInform, Body: "unrelated receipt succeeds",
	})
	if err != nil {
		t.Fatalf("send second: %v", err)
	}
	waitForDeliveryStatus(t, s, second.MessageID, "received", 2*time.Second)
	health := s.Health()
	if degraded, _ := health["delivery_persistence_error"].(bool); !degraded || health["ok"] != false {
		t.Fatalf("unrelated success hid first receipt error: %#v", health)
	}

	if _, err := s.db.Exec(`DROP TRIGGER fail_first_receipt`); err != nil {
		t.Fatalf("drop receipt failure trigger: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close and recover attempting delivery: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("reopen recovered store: %v", err)
	}
	defer reopened.Close()
	waitForDeliveryStatus(t, reopened, first.MessageID, "received", 4*time.Second)
	if health := reopened.Health(); health["ok"] != true {
		t.Fatalf("health stayed degraded after delivery recovery: %#v", health)
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
