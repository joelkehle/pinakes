package bus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// persistedMessage carries the Message fields that are hidden from API
// responses (json:"-") but must survive a restart: without them, in-flight
// messages reload with zero TTL/ack deadlines and the sweep state machine
// cannot expire them. Older state files lack these keys and load as zero,
// matching pre-fix behavior.
type persistedMessage struct {
	Message
	DeliveredAt    time.Time `json:"delivered_at,omitempty"`
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	TTLExpiresAt   time.Time `json:"ttl_expires_at,omitempty"`
	GraceUntil     time.Time `json:"grace_until,omitempty"`
	QueuedForAgent bool      `json:"queued_for_agent,omitempty"`
}

func toPersistedMessage(m Message) persistedMessage {
	return persistedMessage{
		Message:        m,
		DeliveredAt:    m.DeliveredAt,
		LastProgressAt: m.LastProgressAt,
		TTLExpiresAt:   m.TTLExpiresAt,
		GraceUntil:     m.GraceUntil,
		QueuedForAgent: m.QueuedForAgent,
	}
}

func (pm persistedMessage) toMessage() Message {
	m := pm.Message
	m.DeliveredAt = pm.DeliveredAt
	m.LastProgressAt = pm.LastProgressAt
	m.TTLExpiresAt = pm.TTLExpiresAt
	m.GraceUntil = pm.GraceUntil
	m.QueuedForAgent = pm.QueuedForAgent
	return m
}

type persistentState struct {
	NextConversationID   int64                       `json:"next_conversation_id"`
	NextMessageID        int64                       `json:"next_message_id"`
	NextObserveID        int64                       `json:"next_observe_id"`
	PushFailures         int64                       `json:"push_failures"`
	PushSuccesses        int64                       `json:"push_successes"`
	Agents               map[string]Agent            `json:"agents"`
	AgentSecrets         map[string]string           `json:"agent_secrets,omitempty"`
	Conversations        map[string]Conversation     `json:"conversations"`
	Messages             map[string]persistedMessage `json:"messages"`
	ConversationMessages map[string][]string         `json:"conversation_messages"`
	Inboxes              map[string][]InboxEvent     `json:"inboxes"`
	InboxBase            map[string]int              `json:"inbox_base"`
	ObserveEvents        []ObserveEvent              `json:"observe_events"`
	Idempotency          map[string]idempotencyEntry `json:"idempotency"`
}

type PersistentStore struct {
	inner          *Store
	path           string
	agentSecrets   map[string]string
	mu             sync.Mutex
	lastPersistErr string
}

func NewPersistentStore(path string, cfg Config) (*PersistentStore, error) {
	inner := NewStore(cfg)
	ps := &PersistentStore{
		inner:        inner,
		path:         path,
		agentSecrets: map[string]string{},
	}
	if err := ps.load(); err != nil {
		return nil, err
	}
	return ps, nil
}

func (p *PersistentStore) stateSnapshot() persistentState {
	p.inner.mu.Lock()
	defer p.inner.mu.Unlock()

	state := persistentState{
		NextConversationID:   p.inner.nextConversationID,
		NextMessageID:        p.inner.nextMessageID,
		NextObserveID:        p.inner.nextObserveID,
		PushFailures:         p.inner.pushFailures,
		PushSuccesses:        p.inner.pushSuccesses,
		Agents:               map[string]Agent{},
		AgentSecrets:         map[string]string{},
		Conversations:        map[string]Conversation{},
		Messages:             map[string]persistedMessage{},
		ConversationMessages: map[string][]string{},
		Inboxes:              map[string][]InboxEvent{},
		InboxBase:            map[string]int{},
		ObserveEvents:        append([]ObserveEvent{}, p.inner.observeEvents...),
		Idempotency:          map[string]idempotencyEntry{},
	}
	for k, v := range p.inner.agents {
		cp := *v
		state.Agents[k] = cp
	}
	for k, v := range p.agentSecrets {
		state.AgentSecrets[k] = v
	}
	for k, v := range p.inner.conversations {
		cp := *v
		state.Conversations[k] = cp
	}
	for k, v := range p.inner.messages {
		state.Messages[k] = toPersistedMessage(*v)
	}
	for k, v := range p.inner.conversationMessages {
		state.ConversationMessages[k] = append([]string{}, v...)
	}
	for k, v := range p.inner.inboxes {
		state.Inboxes[k] = append([]InboxEvent{}, v...)
	}
	for k, v := range p.inner.inboxBase {
		state.InboxBase[k] = v
	}
	for k, v := range p.inner.idempotency {
		state.Idempotency[k] = v
	}
	return state
}

func (p *PersistentStore) applyState(state persistentState) {
	p.inner.mu.Lock()
	defer p.inner.mu.Unlock()

	p.agentSecrets = map[string]string{}
	for k, v := range state.AgentSecrets {
		agentID := strings.TrimSpace(k)
		if agentID != "" && strings.TrimSpace(v) != "" {
			p.agentSecrets[agentID] = v
		}
	}

	p.inner.nextConversationID = state.NextConversationID
	p.inner.nextMessageID = state.NextMessageID
	p.inner.nextObserveID = state.NextObserveID
	p.inner.pushFailures = state.PushFailures
	p.inner.pushSuccesses = state.PushSuccesses

	p.inner.agents = map[string]*Agent{}
	for k, v := range state.Agents {
		cp := v
		p.inner.agents[k] = &cp
	}
	p.inner.conversations = map[string]*Conversation{}
	for k, v := range state.Conversations {
		cp := v
		p.inner.conversations[k] = &cp
	}
	p.inner.messages = map[string]*Message{}
	for k, v := range state.Messages {
		cp := v.toMessage()
		p.inner.messages[k] = &cp
	}
	p.inner.conversationMessages = map[string][]string{}
	for k, v := range state.ConversationMessages {
		p.inner.conversationMessages[k] = append([]string{}, v...)
	}
	p.inner.inboxes = map[string][]InboxEvent{}
	p.inner.inboxBytes = map[string]int{}
	for k, v := range state.Inboxes {
		p.inner.inboxes[k] = append([]InboxEvent{}, v...)
		bytes := 0
		for _, evt := range v {
			bytes += inboxEventSize(evt)
		}
		p.inner.inboxBytes[k] = bytes
	}
	p.inner.inboxBase = map[string]int{}
	for k, v := range state.InboxBase {
		p.inner.inboxBase[k] = v
	}
	p.inner.observeEvents = append([]ObserveEvent{}, state.ObserveEvents...)
	p.inner.observeBytes = 0
	for i := range p.inner.observeEvents {
		p.inner.observeEvents[i].Size = observeEventSize(p.inner.observeEvents[i])
		p.inner.observeBytes += p.inner.observeEvents[i].Size
	}
	p.inner.idempotency = map[string]idempotencyEntry{}
	for k, v := range state.Idempotency {
		p.inner.idempotency[k] = v
	}
}

func (p *PersistentStore) persist() error {
	if p.path == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Compact marshal: indentation inflated the blob ~40% and doubled the
	// transient allocation on a path that runs after every mutation.
	state := p.stateSnapshot()
	blob, err := json.Marshal(state)
	if err != nil {
		p.lastPersistErr = err.Error()
		return err
	}

	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		p.lastPersistErr = err.Error()
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		p.lastPersistErr = err.Error()
		return err
	}
	if err := os.Rename(tmp, p.path); err != nil {
		p.lastPersistErr = err.Error()
		return err
	}
	p.lastPersistErr = ""
	return nil
}

func (p *PersistentStore) load() error {
	if p.path == "" {
		return nil
	}
	blob, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.Chmod(p.path, 0o600); err != nil {
		return err
	}
	var state persistentState
	if err := json.Unmarshal(blob, &state); err != nil {
		return err
	}
	p.applyState(state)
	return nil
}

func (p *PersistentStore) AgentSecrets() (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make(map[string]string, len(p.agentSecrets))
	for k, v := range p.agentSecrets {
		out[k] = v
	}
	return out, nil
}

func (p *PersistentStore) SetAgentSecret(agentID, secret string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || strings.TrimSpace(secret) == "" {
		return nil
	}

	p.mu.Lock()
	p.agentSecrets[agentID] = secret
	p.mu.Unlock()
	return p.persist()
}

func (p *PersistentStore) persistBestEffort() {
	_ = p.persist()
}

func (p *PersistentStore) RegisterAgent(input RegisterAgentInput) (*Agent, error) {
	out, err := p.inner.RegisterAgent(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return nil, perr
		}
	}
	return out, err
}

func (p *PersistentStore) ListAgents(capability string) []Agent {
	return p.inner.ListAgents(capability)
}

func (p *PersistentStore) CreateConversation(input CreateConversationInput) (*Conversation, error) {
	out, err := p.inner.CreateConversation(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return nil, perr
		}
	}
	return out, err
}

func (p *PersistentStore) ListConversations(filter ListConversationsFilter) []Conversation {
	return p.inner.ListConversations(filter)
}

func (p *PersistentStore) SendMessage(input SendMessageInput) (*Message, bool, error) {
	m, dup, err := p.inner.SendMessage(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return nil, false, perr
		}
	}
	return m, dup, err
}

// PollInbox persists only when events were delivered: idle long-polls
// previously re-serialized the entire state every wake, which dominated CPU
// and transient memory. Poll-time inbox reclamation that goes unpersisted is
// harmless — it re-runs on the next poll after a reload.
func (p *PersistentStore) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	events, cursor, err := p.inner.PollInbox(input)
	if err == nil && len(events) > 0 {
		p.persistBestEffort()
	}
	return events, cursor, err
}

func (p *PersistentStore) Ack(input AckInput) error {
	err := p.inner.Ack(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return perr
		}
	}
	return err
}

func (p *PersistentStore) PostEvent(input EventInput) error {
	err := p.inner.PostEvent(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return perr
		}
	}
	return err
}

func (p *PersistentStore) Inject(input InjectInput) (*Message, error) {
	m, err := p.inner.Inject(input)
	if err == nil {
		if perr := p.persist(); perr != nil {
			return nil, perr
		}
	}
	return m, err
}

func (p *PersistentStore) ListConversationMessages(input ListConversationMessagesInput) (string, []Message, int, error) {
	return p.inner.ListConversationMessages(input)
}

func (p *PersistentStore) ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64) {
	return p.inner.ObserveSince(afterID, filter, wait)
}

func (p *PersistentStore) Health() map[string]any {
	out := p.inner.Health()
	if msg := p.lastPersistError(); msg != "" {
		out["persist_error"] = msg
	}
	return out
}

func (p *PersistentStore) Metrics() string {
	return p.inner.Metrics()
}

func (p *PersistentStore) SystemStatus() map[string]any {
	out := p.inner.SystemStatus()
	if msg := p.lastPersistError(); msg != "" {
		out["persist_error"] = msg
	}
	return out
}

func (p *PersistentStore) lastPersistError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastPersistErr
}

var _ AgentSecretStore = (*PersistentStore)(nil)
