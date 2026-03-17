package bus

import (
	"strconv"
	"testing"
	"time"
)

func TestObserveTrimAndResume(t *testing.T) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	s := NewStore(Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		MaxObserveEvents:       5,
		Clock: func() time.Time {
			return now
		},
	})
	registerPair(t, s, 60, 60)

	for i := 0; i < 6; i++ {
		_, _, err := s.SendMessage(SendMessageInput{
			To:        "b",
			From:      "a",
			RequestID: "rid-observe-" + strconv.Itoa(i),
			Type:      MessageTypeRequest,
			Body:      "hello",
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	events, last := s.ObserveSince(0, ObserveFilter{}, 0)
	if len(events) != 5 {
		t.Fatalf("expected exactly 5 retained observe events, got %d", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Fatalf("observe IDs must be strictly increasing: prev=%d curr=%d", events[i-1].ID, events[i].ID)
		}
	}
	if events[0].ID <= 2 {
		t.Fatalf("expected older observe events to be trimmed, first ID=%d", events[0].ID)
	}

	events2, last2 := s.ObserveSince(last, ObserveFilter{}, 0)
	if len(events2) != 0 {
		t.Fatalf("expected no additional events when resuming at latest cursor, got %d", len(events2))
	}
	if last2 != last {
		t.Fatalf("expected cursor to remain %d, got %d", last, last2)
	}
}
