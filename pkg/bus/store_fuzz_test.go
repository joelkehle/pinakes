package bus

import "testing"

func FuzzSendMessageDoesNotPanic(f *testing.F) {
	f.Add("b", "a", "rid-1", "request", "hello")
	f.Add("b", "a", "", "request", "hello")
	f.Add("", "a", "rid-2", "request", "hello")
	f.Add("b", "", "rid-3", "request", "hello")
	f.Add("b", "a", "rid-4", "unknown", "hello")
	f.Add("b", "a", "rid-5", "inform", "")

	f.Fuzz(func(t *testing.T, to, from, requestID, typ, body string) {
		s, _ := newTestStore(t)
		registerPair(t, s, 60, 60)

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("SendMessage panicked: %v", r)
			}
		}()

		_, _, _ = s.SendMessage(SendMessageInput{
			To:        to,
			From:      from,
			RequestID: requestID,
			Type:      MessageType(typ),
			Body:      body,
		})
	})
}
