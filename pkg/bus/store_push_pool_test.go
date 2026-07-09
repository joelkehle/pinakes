package bus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPushQueueDefaults(t *testing.T) {
	s := NewStore(Config{})

	if got := cap(s.pushQueue); got != 256 {
		t.Fatalf("push queue cap=%d want=256", got)
	}
	if got := s.cfg.PushWorkers; got != 4 {
		t.Fatalf("push workers=%d want=4", got)
	}
}

// TestPushQueueDropsWhenFull wedges the single push worker on a callback
// target that never responds, fills the tiny queue, and asserts the overflow
// sends are dropped (counted as push failures) without blocking SendMessage.
func TestPushQueueDropsWhenFull(t *testing.T) {
	const (
		queueSize = 2
		drops     = 3
	)

	firstCallback := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case firstCallback <- struct{}{}:
		default:
		}
		<-releaseCallback
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(releaseCallback)
		callback.Close()
	})

	s := NewStore(Config{
		PushMaxAttempts: 1,
		PushBaseBackoff: time.Millisecond,
		PushQueueSize:   queueSize,
		PushWorkers:     1,
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID:    "ucla.sender",
		Mode:       AgentModePull,
		TTLSeconds: 60,
	}); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID:     "ucla.push-target",
		Mode:        AgentModePush,
		CallbackURL: callback.URL,
		TTLSeconds:  60,
	}); err != nil {
		t.Fatalf("register push target: %v", err)
	}

	send := func(i int) {
		t.Helper()
		if _, _, err := s.SendMessage(SendMessageInput{
			To:        "ucla.push-target",
			From:      "ucla.sender",
			RequestID: fmt.Sprintf("rid-%d", i),
			Type:      MessageTypeRequest,
			Body:      "hello push",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// First send: wait until the lone worker is wedged inside the callback
	// so the subsequent sends fill the queue deterministically.
	send(0)
	select {
	case <-firstCallback:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first push callback")
	}

	// queueSize sends fill the queue; the next `drops` sends must be
	// dropped without blocking the API path.
	start := time.Now()
	for i := 1; i <= queueSize+drops; i++ {
		send(i)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("send loop took %s, want well under 1s", elapsed)
	}

	s.mu.Lock()
	failures := s.pushFailures
	s.mu.Unlock()
	if failures != drops {
		t.Fatalf("push failures=%d want=%d", failures, drops)
	}
}
