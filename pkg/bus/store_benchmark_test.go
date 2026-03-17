package bus

import (
	"strconv"
	"testing"
	"time"
)

func BenchmarkSendMessage(b *testing.B) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
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
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		b.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		b.Fatalf("register b: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := s.SendMessage(SendMessageInput{
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
}
