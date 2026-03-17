package bus

import "time"

type AgentMode string

const (
	AgentModePull AgentMode = "pull"
	AgentModePush AgentMode = "push"
)

type MessageType string

const (
	MessageTypeRequest  MessageType = "request"
	MessageTypeResponse MessageType = "response"
	MessageTypeInform   MessageType = "inform"
)

type MessageState string

const (
	StatePending    MessageState = "pending"
	StateWaitingAck MessageState = "waiting"
	StateExecuting  MessageState = "executing"
	StateCompleted  MessageState = "completed"
	StateRejected   MessageState = "rejected"
	StateError      MessageState = "error"
)

type AgentStatus string

const (
	AgentStatusActive  AgentStatus = "active"
	AgentStatusExpired AgentStatus = "expired"
)

type EventType string

const (
	ObserveMessage         EventType = "message"
	ObserveAck             EventType = "ack"
	ObserveProgress        EventType = "progress"
	ObserveStateChange     EventType = "state_change"
	ObserveAgentRegistered EventType = "agent_registered"
	ObserveAgentExpired    EventType = "agent_expired"
	ObserveHumanInjection  EventType = "human_injection"
)

type Attachment struct {
	URL         string `json:"url"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type Agent struct {
	AgentID      string      `json:"agent_id"`
	Capabilities []string    `json:"capabilities"`
	Description  string      `json:"description,omitempty"`
	Mode         AgentMode   `json:"mode"`
	CallbackURL  string      `json:"callback_url,omitempty"`
	Status       AgentStatus `json:"status"`
	RegisteredAt time.Time   `json:"registered_at"`
	ExpiresAt    time.Time   `json:"expires_at"`
	TTLSeconds   int         `json:"-"`
}

type Conversation struct {
	ConversationID string    `json:"conversation_id"`
	Title          string    `json:"title,omitempty"`
	Participants   []string  `json:"participants,omitempty"`
	Status         string    `json:"status"`
	MessageCount   int       `json:"message_count"`
	CreatedAt      time.Time `json:"created_at"`
	LastMessageAt  time.Time `json:"last_message_at"`
	Meta           any       `json:"meta,omitempty"`
}

type Message struct {
	MessageID      string       `json:"message_id"`
	Type           MessageType  `json:"type"`
	From           string       `json:"from"`
	To             string       `json:"to,omitempty"`
	ConversationID string       `json:"conversation_id,omitempty"`
	RequestID      string       `json:"request_id"`
	InReplyTo      string       `json:"in_reply_to,omitempty"`
	Body           string       `json:"body"`
	Meta           any          `json:"meta,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	State          MessageState `json:"state,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	DeliveredAt    time.Time    `json:"-"`
	LastProgressAt time.Time    `json:"-"`
	TTLExpiresAt   time.Time    `json:"-"`
	GraceUntil     time.Time    `json:"-"`
	QueuedForAgent bool         `json:"-"`
}

type InboxEvent struct {
	MessageID      string       `json:"message_id"`
	Type           MessageType  `json:"type"`
	From           string       `json:"from"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Body           string       `json:"body"`
	Meta           any          `json:"meta,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
}

type ObserveEvent struct {
	ID             int64     `json:"id"`
	Type           EventType `json:"type"`
	At             time.Time `json:"at"`
	Data           any       `json:"data"`
	ConversationID string    `json:"-"`
	AgentIDs       []string  `json:"-"`
}

type RegisterAgentInput struct {
	AgentID      string
	Capabilities []string
	Description  string
	Mode         AgentMode
	CallbackURL  string
	TTLSeconds   int
}

type CreateConversationInput struct {
	ConversationID string
	Title          string
	Participants   []string
	Meta           any
}

type ListConversationsFilter struct {
	Participant string
	Status      string
}

type SendMessageInput struct {
	To             string
	From           string
	ConversationID string
	RequestID      string
	Type           MessageType
	Body           string
	Meta           any
	Attachments    []Attachment
	TTLSeconds     int
	InReplyTo      string
}

type PollInboxInput struct {
	AgentID string
	Cursor  int
	Wait    time.Duration
}

type AckInput struct {
	AgentID   string
	MessageID string
	Status    string
	Reason    string
}

type EventInput struct {
	ActorAgentID string
	MessageID    string
	Type         string
	Body         string
	Meta         any
}

type InjectInput struct {
	Identity       string
	ConversationID string
	To             string
	Body           string
}

type ListConversationMessagesInput struct {
	ConversationID string
	Cursor         int
	Limit          int
}

type ObserveFilter struct {
	ConversationID string
	AgentID        string
}
