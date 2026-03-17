package bus

import "time"

// API is the storage/service interface used by the HTTP layer.
// It allows swapping in-memory and persistent implementations.
type API interface {
	RegisterAgent(input RegisterAgentInput) (*Agent, error)
	ListAgents(capability string) []Agent
	CreateConversation(input CreateConversationInput) (*Conversation, error)
	ListConversations(filter ListConversationsFilter) []Conversation
	SendMessage(input SendMessageInput) (*Message, bool, error)
	PollInbox(input PollInboxInput) ([]InboxEvent, int, error)
	Ack(input AckInput) error
	PostEvent(input EventInput) error
	Inject(input InjectInput) (*Message, error)
	ListConversationMessages(input ListConversationMessagesInput) (string, []Message, int, error)
	ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64)
	Health() map[string]any
	Metrics() string
	SystemStatus() map[string]any
}
