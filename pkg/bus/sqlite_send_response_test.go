package bus

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSendResponseDoesNotWaitOnDeliveryReceipt reproduces issue #26: an
// already-committed send must return its HTTP response without waiting on any
// delivery side-effect. The hazardous case is a push target whose callback
// wedges the worker pool and fills the bounded delivery channel, so the send
// takes the push enqueue-failure path whose bookkeeping is a SQLite receipt
// write. That write must run OFF the sender's response goroutine.
//
// Before the fix the deferred unlock closure invoked the receipt write inline,
// so holding the write open (testHookPushReceipt) blocked SendMessage
// indefinitely even though the message was already durably committed.
func TestSendResponseDoesNotWaitOnDeliveryReceipt(t *testing.T) {
	// Callback that wedges the lone push worker until released, so the delivery
	// channel stays occupied and later sends overflow it.
	wedged := make(chan struct{}, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case wedged <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(callback.Close)

	cfg := Config{
		PushWorkers:     1,
		PushQueueSize:   1,
		PushMaxAttempts: 1,
		PushBaseBackoff: time.Millisecond,
	}
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "send-response.db"), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		unblock()
		_ = s.Close()
	})

	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "ucla.sender", Mode: AgentModePull, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID: "ucla.push-target", Mode: AgentModePush, CallbackURL: callback.URL, TTLSeconds: 3600,
	}); err != nil {
		t.Fatalf("register push target: %v", err)
	}

	send := func(rid string) (*Message, error) {
		m, _, err := s.SendMessage(SendMessageInput{
			From: "ucla.sender", To: "ucla.push-target",
			RequestID: rid, Type: MessageTypeRequest, Body: "hello push",
		})
		return m, err
	}

	// Send 1 wedges the single worker inside the dead callback.
	if _, err := send("rid-wedge"); err != nil {
		t.Fatalf("send wedge: %v", err)
	}
	select {
	case <-wedged:
	case <-time.After(3 * time.Second):
		t.Fatal("push worker never reached the callback")
	}

	// Send 2 fills the single-slot delivery channel (worker is busy).
	if _, err := send("rid-fill"); err != nil {
		t.Fatalf("send fill: %v", err)
	}

	// Arm the receipt-write hook: the first receipt write (the enqueue-failure
	// bookkeeping for send 3) signals then blocks until released. A later
	// worker/delivery-loop retry may also reach it, so only the first caller
	// blocks and the rest of the store stays live.
	receiptEntered := make(chan struct{})
	receiptRelease := make(chan struct{})
	releaseReceipt := sync.OnceFunc(func() { close(receiptRelease) })
	t.Cleanup(releaseReceipt)
	var hookOnce sync.Once
	s.mu.Lock()
	s.testHookPushReceipt = func() {
		hookOnce.Do(func() {
			close(receiptEntered)
			<-receiptRelease
		})
	}
	s.mu.Unlock()

	// Send 3 overflows the delivery channel, taking the enqueue-failure path.
	// Its response must return even though the receipt write is held open.
	done := make(chan error, 1)
	var overflow *Message
	go func() {
		m, err := send("rid-overflow")
		overflow = m
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("overflow send returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overflow send BLOCKED on the delivery receipt write (issue #26 regression)")
	}

	// Prove the receipt write is genuinely still in flight, i.e. the send
	// really overtook it rather than the write simply being fast.
	select {
	case <-receiptEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("expected an in-flight delivery receipt write for the overflow send")
	}

	// The send was durably committed before the response returned.
	if overflow == nil || overflow.MessageID == "" {
		t.Fatalf("overflow send returned no message: %+v", overflow)
	}
	stored, ok := s.GetMessageForTest(overflow.MessageID)
	if !ok {
		t.Fatalf("overflow message %s not committed", overflow.MessageID)
	}
	if stored.To != "ucla.push-target" || stored.Body != "hello push" {
		t.Fatalf("overflow message persisted wrong: %+v", stored)
	}

	// All three sends produced durable deliveries (none lost) and none silently
	// vanished behind the held receipt write.
	if pending, failed := s.deliveryCounts(); pending+failed != 3 {
		t.Fatalf("deliveries not durable: pending=%d failed=%d want 3 total", pending, failed)
	}

	// Release everything; the store must shut down cleanly in cleanup.
	releaseReceipt()
	unblock()
}
