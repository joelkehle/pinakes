package bus

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestSweepLockedRateLimited asserts that successive sweep calls inside the
// configured SweepMinInterval window skip the expensive body. This is the
// regression guard for the CPU spike observed when buses retained 200k+
// messages and every 100ms long-poll tick scanned them all.
func TestSweepLockedRateLimited(t *testing.T) {
	current := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return current }
	s := NewStore(Config{
		IdempotencyWindow: 24 * time.Hour,
		SweepMinInterval:  500 * time.Millisecond,
		Clock:             clock,
	})

	// First call always sweeps (lastSweepAt is zero).
	s.mu.Lock()
	s.sweepLocked(current)
	s.mu.Unlock()
	if got := s.sweepRan.Load(); got != 1 {
		t.Fatalf("expected 1 sweep run, got %d", got)
	}
	if got := s.sweepSkipped.Load(); got != 0 {
		t.Fatalf("expected 0 skipped sweeps, got %d", got)
	}

	// Same now → skip.
	s.mu.Lock()
	s.sweepLocked(current)
	s.mu.Unlock()
	if got := s.sweepRan.Load(); got != 1 {
		t.Fatalf("expected 1 sweep run after rate-limited call, got %d", got)
	}
	if got := s.sweepSkipped.Load(); got != 1 {
		t.Fatalf("expected 1 skipped sweep, got %d", got)
	}

	// Within window → skip.
	current = current.Add(200 * time.Millisecond)
	s.mu.Lock()
	s.sweepLocked(current)
	s.mu.Unlock()
	if got := s.sweepRan.Load(); got != 1 {
		t.Fatalf("expected 1 sweep run within window, got %d", got)
	}
	if got := s.sweepSkipped.Load(); got != 2 {
		t.Fatalf("expected 2 skipped sweeps, got %d", got)
	}

	// Past window → runs.
	current = current.Add(400 * time.Millisecond)
	s.mu.Lock()
	s.sweepLocked(current)
	s.mu.Unlock()
	if got := s.sweepRan.Load(); got != 2 {
		t.Fatalf("expected 2 sweep runs after window, got %d", got)
	}
}

// TestPollInboxIdleLongPollCheapWithBacklog is the headline regression: idle
// PollInbox returning empty after wait must not run the full O(messages)
// sweep on every wake. Before the fix, the 100ms sleep loop swept ~10
// times/sec — at 300k retained messages that exhausted a CPU core.
func TestPollInboxIdleLongPollCheapWithBacklog(t *testing.T) {
	current := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	mu := sync.Mutex{}
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	advance := func(d time.Duration) {
		mu.Lock()
		current = current.Add(d)
		mu.Unlock()
	}

	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           5 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		SweepMinInterval:       250 * time.Millisecond,
		Clock:                  clock,
	})

	// Two heartbeat agents — sender registers and sends one message per
	// iteration. Recipient stays idle.
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register b: %v", err)
	}

	// Build a retained backlog. 2_000 is enough to make a per-call O(N)
	// sweep visible; we don't need 300k to verify rate-limiting kicks in.
	for i := 0; i < 2000; i++ {
		_, _, err := s.SendMessage(SendMessageInput{
			To:        "b",
			From:      "a",
			RequestID: "bench-" + strconv.Itoa(i),
			Type:      MessageTypeRequest,
			Body:      "payload",
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		// Drain the inbox so PollInbox(wait>0) actually waits.
		_, _, _ = s.PollInbox(PollInboxInput{AgentID: "b", Cursor: i + 1, Wait: 0})
	}

	sweepRanBefore := s.sweepRan.Load()

	// Long-poll for 100ms of real time — no messages should arrive, the
	// notifier path should not wake spuriously, and the rate-limiter must
	// gate any extra sweep calls.
	done := make(chan struct{})
	go func() {
		defer close(done)
		events, _, err := s.PollInbox(PollInboxInput{
			AgentID: "b",
			Cursor:  2000,
			Wait:    100 * time.Millisecond,
		})
		if err != nil {
			t.Errorf("poll: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("expected 0 events, got %d", len(events))
		}
	}()

	// Move the simulated clock past the deadline so the timer fires.
	time.Sleep(50 * time.Millisecond)
	advance(150 * time.Millisecond)
	<-done

	// At most one extra sweep — the entry sweep. The fix means we do NOT
	// re-sweep on every 100ms tick anymore.
	sweepRanAfter := s.sweepRan.Load()
	if delta := sweepRanAfter - sweepRanBefore; delta > 1 {
		t.Fatalf("idle long-poll triggered %d sweeps; expected <= 1", delta)
	}
}

// TestPollInboxWakesOnAppend confirms the notifier path replaces the prior
// 100ms sleep: a SendMessage during a long-poll must wake the poller
// promptly without depending on the timer firing.
func TestPollInboxWakesOnAppend(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           5 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		SweepMinInterval:       250 * time.Millisecond,
		// Real time clock so the wait deadline behaves naturally; the
		// notifier should fire well before the deadline.
	})
	_ = now
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register b: %v", err)
	}

	type result struct {
		events []InboxEvent
		err    error
		took   time.Duration
	}
	out := make(chan result, 1)

	start := time.Now()
	go func() {
		events, _, err := s.PollInbox(PollInboxInput{
			AgentID: "b",
			Cursor:  0,
			Wait:    2 * time.Second,
		})
		out <- result{events: events, err: err, took: time.Since(start)}
	}()

	time.Sleep(50 * time.Millisecond)
	if _, _, err := s.SendMessage(SendMessageInput{
		To:        "b",
		From:      "a",
		RequestID: "wake",
		Type:      MessageTypeRequest,
		Body:      "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("poll err: %v", r.err)
		}
		if len(r.events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(r.events))
		}
		// Must wake well before the 2s deadline. The prior implementation
		// used a 100ms sleep loop so worst case was ~100ms; the notifier
		// path should beat that with margin. Allow 500ms for slow CI.
		if r.took > 500*time.Millisecond {
			t.Fatalf("poll took %s — expected sub-second wake via notifier", r.took)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not return")
	}
}

// TestObserveSinceWakesOnPublish confirms the observe notifier path also
// replaces the 100ms sleep loop.
func TestObserveSinceWakesOnPublish(t *testing.T) {
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           5 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		SweepMinInterval:       250 * time.Millisecond,
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 3600}); err != nil {
		t.Fatalf("register b: %v", err)
	}

	// Capture current cursor — registration events already published.
	_, cursor := s.ObserveSince(0, ObserveFilter{}, 0)

	type result struct {
		events []ObserveEvent
		took   time.Duration
	}
	out := make(chan result, 1)
	start := time.Now()
	go func() {
		events, _ := s.ObserveSince(cursor, ObserveFilter{}, 2*time.Second)
		out <- result{events: events, took: time.Since(start)}
	}()

	time.Sleep(50 * time.Millisecond)
	if _, _, err := s.SendMessage(SendMessageInput{
		To:        "b",
		From:      "a",
		RequestID: "obs-wake",
		Type:      MessageTypeRequest,
		Body:      "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case r := <-out:
		if len(r.events) == 0 {
			t.Fatalf("expected at least one observe event")
		}
		if r.took > 500*time.Millisecond {
			t.Fatalf("observe took %s — expected sub-second wake via notifier", r.took)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observe did not return")
	}
}

// BenchmarkIdlePollInboxWithBacklog measures the per-op cost of an idle
// PollInbox(wait=0) call against a store with a large retained-message
// backlog. Before the rate-limit + notifier rework, this was O(messages)
// per call because sweepLocked walked the entire message map on every
// invocation.
func BenchmarkIdlePollInboxWithBacklog(b *testing.B) {
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           60 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		SweepMinInterval:       250 * time.Millisecond,
		MaxInboxEventsPerAgent: 1,
		MaxObserveEvents:       1000,
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 3600}); err != nil {
		b.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 3600}); err != nil {
		b.Fatalf("register b: %v", err)
	}

	// Pre-populate retained messages so sweepLocked has real work to skip.
	const backlog = 50000
	for i := 0; i < backlog; i++ {
		if _, _, err := s.SendMessage(SendMessageInput{
			To:        "b",
			From:      "a",
			RequestID: "bench-backlog-" + strconv.Itoa(i),
			Type:      MessageTypeRequest,
			Body:      "x",
		}); err != nil {
			b.Fatalf("send: %v", err)
		}
	}
	// Drain b's inbox cursor so PollInbox returns empty.
	_, cursor, err := s.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
	if err != nil {
		b.Fatalf("drain: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := s.PollInbox(PollInboxInput{
			AgentID: "b",
			Cursor:  cursor + backlog,
			Wait:    0,
		})
		if err != nil {
			b.Fatalf("poll: %v", err)
		}
	}
}

// BenchmarkIdleObserveSinceAtCursor measures the per-op cost of an
// ObserveSince call that returns no new events. With the binary-search
// fix this should be O(log N) on a full observe ring; the prior
// implementation was O(N).
func BenchmarkIdleObserveSinceAtCursor(b *testing.B) {
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           60 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		SweepMinInterval:       250 * time.Millisecond,
		MaxObserveEvents:       50000,
		MaxInboxEventsPerAgent: 1,
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 3600}); err != nil {
		b.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 3600}); err != nil {
		b.Fatalf("register b: %v", err)
	}
	// Pack the observe ring.
	for i := 0; i < 50000; i++ {
		if _, _, err := s.SendMessage(SendMessageInput{
			To:        "b",
			From:      "a",
			RequestID: "bench-obs-" + strconv.Itoa(i),
			Type:      MessageTypeRequest,
			Body:      "x",
		}); err != nil {
			b.Fatalf("send: %v", err)
		}
	}
	_, cursor := s.ObserveSince(0, ObserveFilter{}, 0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ObserveSince(cursor, ObserveFilter{}, 0)
	}
}
