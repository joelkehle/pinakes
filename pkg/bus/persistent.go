package bus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type persistentState struct {
	NextConversationID   int64                       `json:"next_conversation_id"`
	NextMessageID        int64                       `json:"next_message_id"`
	NextObserveID        int64                       `json:"next_observe_id"`
	PushFailures         int64                       `json:"push_failures"`
	PushSuccesses        int64                       `json:"push_successes"`
	Agents               map[string]Agent            `json:"agents"`
	Conversations        map[string]Conversation     `json:"conversations"`
	Messages             map[string]Message          `json:"messages"`
	ConversationMessages map[string][]string         `json:"conversation_messages"`
	Inboxes              map[string][]InboxEvent     `json:"inboxes"`
	InboxBase            map[string]int              `json:"inbox_base"`
	ObserveEvents        []ObserveEvent              `json:"observe_events"`
	Idempotency          map[string]idempotencyEntry `json:"idempotency"`
}

type PersistentStore struct {
	inner          *Store
	path           string
	mu             sync.Mutex
	lastPersistErr string
}

func NewPersistentStore(path string, cfg Config) (*PersistentStore, error) {
	inner := NewStore(cfg)
	ps := &PersistentStore{
		inner: inner,
		path:  path,
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
		Conversations:        map[string]Conversation{},
		Messages:             map[string]Message{},
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
	for k, v := range p.inner.conversations {
		cp := *v
		state.Conversations[k] = cp
	}
	for k, v := range p.inner.messages {
		cp := *v
		state.Messages[k] = cp
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
		cp := v
		p.inner.messages[k] = &cp
	}
	p.inner.conversationMessages = map[string][]string{}
	for k, v := range state.ConversationMessages {
		p.inner.conversationMessages[k] = append([]string{}, v...)
	}
	p.inner.inboxes = map[string][]InboxEvent{}
	for k, v := range state.Inboxes {
		p.inner.inboxes[k] = append([]InboxEvent{}, v...)
	}
	p.inner.inboxBase = map[string]int{}
	for k, v := range state.InboxBase {
		p.inner.inboxBase[k] = v
	}
	p.inner.observeEvents = append([]ObserveEvent{}, state.ObserveEvents...)
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

	state := p.stateSnapshot()
	blob, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		p.lastPersistErr = err.Error()
		return err
	}

	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		p.lastPersistErr = err.Error()
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
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
	var state persistentState
	if err := json.Unmarshal(blob, &state); err != nil {
		return err
	}
	p.applyState(state)
	return nil
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
	out := p.inner.ListAgents(capability)
	p.persistBestEffort()
	return out
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
	out := p.inner.ListConversations(filter)
	p.persistBestEffort()
	return out
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

func (p *PersistentStore) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	events, cursor, err := p.inner.PollInbox(input)
	p.persistBestEffort()
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
	cid, messages, cursor, err := p.inner.ListConversationMessages(input)
	p.persistBestEffort()
	return cid, messages, cursor, err
}

func (p *PersistentStore) ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64) {
	events, last := p.inner.ObserveSince(afterID, filter, wait)
	p.persistBestEffort()
	return events, last
}

func (p *PersistentStore) Health() map[string]any {
	out := p.inner.Health()
	p.persistBestEffort()
	if p.lastPersistErr != "" {
		out["persist_error"] = p.lastPersistErr
	}
	return out
}

func (p *PersistentStore) Metrics() string {
	out := p.inner.Metrics()
	p.persistBestEffort()
	return out
}

func (p *PersistentStore) SystemStatus() map[string]any {
	out := p.inner.SystemStatus()
	p.persistBestEffort()
	if p.lastPersistErr != "" {
		out["persist_error"] = p.lastPersistErr
	}
	return out
}
