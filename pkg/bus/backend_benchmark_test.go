package bus

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type backendFactory struct {
	name string
	make func(b *testing.B) API
}

func benchmarkConfig() Config {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	return Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           2 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}
}

func benchmarkBackends(cfg Config) []backendFactory {
	return []backendFactory{
		{
			name: "Memory",
			make: func(b *testing.B) API {
				return NewStore(cfg)
			},
		},
		{
			name: "JSON",
			make: func(b *testing.B) API {
				s, err := NewPersistentStore(filepath.Join(b.TempDir(), "state.json"), cfg)
				if err != nil {
					b.Fatalf("new persistent store: %v", err)
				}
				return s
			},
		},
		{
			name: "SQLite",
			make: func(b *testing.B) API {
				s, err := NewSQLiteStore(filepath.Join(b.TempDir(), "bus.db"), cfg)
				if err != nil {
					b.Fatalf("new sqlite store: %v", err)
				}
				b.Cleanup(func() { _ = s.Close() })
				return s
			},
		},
	}
}

func newBenchmarkBackend(b *testing.B, factory backendFactory) API {
	b.Helper()

	store := factory.make(b)
	if _, err := store.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		b.Fatalf("register a: %v", err)
	}
	if _, err := store.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		b.Fatalf("register b: %v", err)
	}
	return store
}

func BenchmarkBackendSendMessage(b *testing.B) {
	for _, factory := range benchmarkBackends(benchmarkConfig()) {
		b.Run(factory.name, func(b *testing.B) {
			store := newBenchmarkBackend(b, factory)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, err := store.SendMessage(SendMessageInput{
					To:        "b",
					From:      "a",
					RequestID: "rid-bench-" + strconv.Itoa(i),
					Type:      MessageTypeRequest,
					Body:      "payload",
				})
				if err != nil {
					b.Fatalf("send failed at i=%d: %v", i, err)
				}
			}
		})
	}
}

func BenchmarkBackendPollInbox(b *testing.B) {
	for _, factory := range benchmarkBackends(benchmarkConfig()) {
		b.Run(factory.name, func(b *testing.B) {
			store := newBenchmarkBackend(b, factory)
			for i := 0; i < 100; i++ {
				if _, _, err := store.SendMessage(SendMessageInput{
					To:        "b",
					From:      "a",
					RequestID: "rid-poll-" + strconv.Itoa(i),
					Type:      MessageTypeRequest,
					Body:      "payload",
				}); err != nil {
					b.Fatalf("seed send failed at i=%d: %v", i, err)
				}
			}

			events, _, err := store.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0})
			if err != nil {
				b.Fatalf("sanity poll failed: %v", err)
			}
			if len(events) == 0 {
				b.Fatalf("sanity poll returned no events")
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Cursor 0 keeps the reclaim base fixed, so this repeatedly reads the same backlog.
				if _, _, err := store.PollInbox(PollInboxInput{AgentID: "b", Cursor: 0, Wait: 0}); err != nil {
					b.Fatalf("poll failed at i=%d: %v", i, err)
				}
			}
		})
	}
}

func BenchmarkBackendRoundTrip(b *testing.B) {
	for _, factory := range benchmarkBackends(benchmarkConfig()) {
		b.Run(factory.name, func(b *testing.B) {
			store := newBenchmarkBackend(b, factory)
			cursor := 0

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, _, err := store.SendMessage(SendMessageInput{
					To:        "b",
					From:      "a",
					RequestID: "rid-rt-" + strconv.Itoa(i),
					Type:      MessageTypeRequest,
					Body:      "payload",
				})
				if err != nil {
					b.Fatalf("send failed at i=%d: %v", i, err)
				}

				_, next, err := store.PollInbox(PollInboxInput{AgentID: "b", Cursor: cursor, Wait: 0})
				if err != nil {
					b.Fatalf("poll failed at i=%d: %v", i, err)
				}
				cursor = next

				if err := store.Ack(AckInput{AgentID: "b", MessageID: m.MessageID, Status: "accepted"}); err != nil {
					b.Fatalf("ack failed at i=%d: %v", i, err)
				}
			}
		})
	}
}
