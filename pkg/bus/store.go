package bus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type snapshotCounts struct {
	active        int
	expired       int
	conversations int
	messages      int
	observeEvents int
	pushSuccesses int64
	pushFailures  int64
}

type Config struct {
	GracePeriod            time.Duration
	ProgressMinInterval    time.Duration
	IdempotencyWindow      time.Duration
	InboxWaitMax           time.Duration
	AckTimeout             time.Duration
	DefaultMessageTTL      time.Duration
	DefaultRegistrationTTL time.Duration
	PushMaxAttempts        int
	PushBaseBackoff        time.Duration
	MaxInboxEventsPerAgent int
	MaxObserveEvents       int
	Clock                  func() time.Time
}

type idempotencyEntry struct {
	MessageID string
	CreatedAt time.Time
}

type Store struct {
	mu sync.Mutex

	cfg Config

	nextConversationID int64
	nextMessageID      int64
	nextObserveID      int64

	agents        map[string]*Agent
	conversations map[string]*Conversation
	messages      map[string]*Message

	conversationMessages map[string][]string
	inboxes              map[string][]InboxEvent
	inboxBase            map[string]int
	observeEvents        []ObserveEvent
	idempotency          map[string]idempotencyEntry

	humanAllowlist map[string]struct{}
	httpClient     *http.Client
	logger         *log.Logger
	pushFailures   int64
	pushSuccesses  int64
}

func NewStore(cfg Config) *Store {
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 30 * time.Second
	}
	if cfg.ProgressMinInterval <= 0 {
		cfg.ProgressMinInterval = 2 * time.Second
	}
	if cfg.IdempotencyWindow <= 0 {
		cfg.IdempotencyWindow = 24 * time.Hour
	}
	if cfg.InboxWaitMax <= 0 {
		cfg.InboxWaitMax = 60 * time.Second
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = 10 * time.Second
	}
	if cfg.DefaultMessageTTL <= 0 {
		cfg.DefaultMessageTTL = 600 * time.Second
	}
	if cfg.DefaultRegistrationTTL <= 0 {
		cfg.DefaultRegistrationTTL = 60 * time.Second
	}
	if cfg.PushMaxAttempts <= 0 {
		cfg.PushMaxAttempts = 3
	}
	if cfg.PushBaseBackoff <= 0 {
		cfg.PushBaseBackoff = 500 * time.Millisecond
	}
	if cfg.MaxInboxEventsPerAgent <= 0 {
		cfg.MaxInboxEventsPerAgent = 10000
	}
	if cfg.MaxObserveEvents <= 0 {
		cfg.MaxObserveEvents = 50000
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}

	allowlist := map[string]struct{}{}
	for _, raw := range strings.Split(os.Getenv("HUMAN_ALLOWLIST"), ",") {
		v := strings.TrimSpace(raw)
		if v != "" {
			allowlist[v] = struct{}{}
		}
	}

	return &Store{
		cfg:                  cfg,
		agents:               map[string]*Agent{},
		conversations:        map[string]*Conversation{},
		messages:             map[string]*Message{},
		conversationMessages: map[string][]string{},
		inboxes:              map[string][]InboxEvent{},
		inboxBase:            map[string]int{},
		observeEvents:        []ObserveEvent{},
		idempotency:          map[string]idempotencyEntry{},
		humanAllowlist:       allowlist,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: log.New(os.Stdout, "pinakes ", log.LstdFlags),
	}
}

func (s *Store) now() time.Time {
	return s.cfg.Clock().UTC()
}

func dedupeKey(from, to, requestID string) string {
	return from + "\x1f" + to + "\x1f" + requestID
}

func normalizeBuildInfo(in *BuildInfo) *BuildInfo {
	if in == nil {
		return nil
	}
	out := &BuildInfo{
		Commit: strings.TrimSpace(in.Commit),
		Dirty:  in.Dirty,
	}
	if out.Commit == "" && !out.Dirty {
		return nil
	}
	return out
}

func normalizeAgentMeta(in *AgentMeta) *AgentMeta {
	if in == nil {
		return nil
	}
	out := &AgentMeta{
		Owner:     strings.TrimSpace(in.Owner),
		Repo:      strings.TrimSpace(in.Repo),
		HealthURL: strings.TrimSpace(in.HealthURL),
	}
	for _, dep := range in.Dependencies {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		out.Dependencies = append(out.Dependencies, dep)
	}
	if out.Owner == "" && out.Repo == "" && out.HealthURL == "" && len(out.Dependencies) == 0 {
		return nil
	}
	return out
}

func isTerminal(state MessageState) bool {
	switch state {
	case StateCompleted, StateRejected, StateError:
		return true
	default:
		return false
	}
}

func (s *Store) appendInboxLocked(agentID string, evt InboxEvent) {
	s.inboxes[agentID] = append(s.inboxes[agentID], evt)
	max := s.cfg.MaxInboxEventsPerAgent
	if max > 0 && len(s.inboxes[agentID]) > max {
		drop := len(s.inboxes[agentID]) - max
		s.inboxes[agentID] = append([]InboxEvent{}, s.inboxes[agentID][drop:]...)
		s.inboxBase[agentID] += drop
	}
}

func (s *Store) trimObserveLocked() {
	max := s.cfg.MaxObserveEvents
	if max > 0 && len(s.observeEvents) > max {
		drop := len(s.observeEvents) - max
		s.observeEvents = append([]ObserveEvent{}, s.observeEvents[drop:]...)
	}
}

func (s *Store) sendPushCallback(url string, payload map[string]any) {
	blob, err := json.Marshal(payload)
	if err != nil {
		s.logger.Printf("push delivery marshal failed: %v", err)
		return
	}
	backoff := s.cfg.PushBaseBackoff
	for attempt := 1; attempt <= s.cfg.PushMaxAttempts; attempt++ {
		req, reqErr := http.NewRequest(http.MethodPost, url, bytes.NewReader(blob))
		if reqErr != nil {
			s.logger.Printf("push delivery request build failed attempt=%d url=%s err=%v", attempt, url, reqErr)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := s.httpClient.Do(req)
		if doErr == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			s.logger.Printf("push delivery success attempt=%d url=%s status=%d", attempt, url, resp.StatusCode)
			_ = resp.Body.Close()
			s.mu.Lock()
			s.pushSuccesses++
			s.mu.Unlock()
			return
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		s.logger.Printf("push delivery failed attempt=%d url=%s status=%d err=%v", attempt, url, status, doErr)
		if attempt == s.cfg.PushMaxAttempts {
			break
		}
		time.Sleep(backoff)
		backoff = backoff * 2
	}
	s.logger.Printf("push delivery exhausted retries url=%s attempts=%d", url, s.cfg.PushMaxAttempts)
	s.mu.Lock()
	s.pushFailures++
	s.mu.Unlock()
}

func (s *Store) publishLocked(eventType EventType, data any, conversationID string, agentIDs []string, at time.Time) {
	s.nextObserveID++
	s.observeEvents = append(s.observeEvents, ObserveEvent{
		ID:             s.nextObserveID,
		Type:           eventType,
		At:             at,
		Data:           data,
		ConversationID: conversationID,
		AgentIDs:       agentIDs,
	})
	s.trimObserveLocked()
}

func (s *Store) ensureConversationLocked(input CreateConversationInput, now time.Time) *Conversation {
	id := strings.TrimSpace(input.ConversationID)
	if id == "" {
		s.nextConversationID++
		id = fmt.Sprintf("c-%06d", s.nextConversationID)
	}
	if existing, ok := s.conversations[id]; ok {
		return existing
	}
	c := &Conversation{
		ConversationID: id,
		Title:          strings.TrimSpace(input.Title),
		Participants:   append([]string{}, input.Participants...),
		Status:         "active",
		CreatedAt:      now,
		LastMessageAt:  now,
		Meta:           input.Meta,
	}
	s.conversations[id] = c
	return c
}

func (s *Store) sweepLocked(now time.Time) {
	for k, v := range s.idempotency {
		if now.Sub(v.CreatedAt) > s.cfg.IdempotencyWindow {
			delete(s.idempotency, k)
		}
	}

	for _, agent := range s.agents {
		if agent.Status == AgentStatusActive && now.After(agent.ExpiresAt) {
			agent.Status = AgentStatusExpired
			s.publishLocked(
				ObserveAgentExpired,
				map[string]any{"agent_id": agent.AgentID, "at": now},
				"",
				[]string{agent.AgentID},
				now,
			)
		}
	}

	for _, m := range s.messages {
		if m.Type != MessageTypeRequest || isTerminal(m.State) {
			continue
		}
		if !m.TTLExpiresAt.IsZero() && now.After(m.TTLExpiresAt) {
			from := m.State
			m.State = StateError
			s.publishLocked(
				ObserveStateChange,
				map[string]any{
					"message_id": m.MessageID,
					"from_state": from,
					"to_state":   StateError,
					"at":         now,
					"error":      "ttl timeout",
				},
				m.ConversationID,
				[]string{m.From, m.To},
				now,
			)
			continue
		}

		if m.State == StateWaitingAck && !m.DeliveredAt.IsZero() && now.Sub(m.DeliveredAt) > s.cfg.AckTimeout {
			from := m.State
			m.State = StateError
			s.publishLocked(
				ObserveStateChange,
				map[string]any{
					"message_id": m.MessageID,
					"from_state": from,
					"to_state":   StateError,
					"at":         now,
					"error":      "ack timeout",
				},
				m.ConversationID,
				[]string{m.From, m.To},
				now,
			)
			continue
		}

		if m.QueuedForAgent {
			target, ok := s.agents[m.To]
			if ok && target.Status == AgentStatusActive {
				m.QueuedForAgent = false
				m.State = StateWaitingAck
				m.DeliveredAt = now
				s.appendInboxLocked(m.To, InboxEvent{
					MessageID:      m.MessageID,
					Type:           m.Type,
					From:           m.From,
					ConversationID: m.ConversationID,
					Body:           m.Body,
					Meta:           m.Meta,
					Attachments:    append([]Attachment{}, m.Attachments...),
					CreatedAt:      m.CreatedAt,
				})
				continue
			}
			if !m.GraceUntil.IsZero() && now.After(m.GraceUntil) {
				from := m.State
				m.QueuedForAgent = false
				m.State = StateError
				s.publishLocked(
					ObserveStateChange,
					map[string]any{
						"message_id": m.MessageID,
						"from_state": from,
						"to_state":   StateError,
						"at":         now,
						"error":      "target agent did not re-register in grace period",
					},
					m.ConversationID,
					[]string{m.From, m.To},
					now,
				)
			}
		}
	}
}

func (s *Store) RegisterAgent(input RegisterAgentInput) (*Agent, error) {
	now := s.now()
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, newError(CodeValidation, "agent_id is required", false, 0)
	}
	mode := input.Mode
	if mode == "" {
		mode = AgentModePull
	}
	if mode != AgentModePull && mode != AgentModePush {
		return nil, newError(CodeValidation, "mode must be pull or push", false, 0)
	}
	if mode == AgentModePush && strings.TrimSpace(input.CallbackURL) == "" {
		return nil, newError(CodeValidation, "callback_url required for push mode", false, 0)
	}
	agentClass := strings.TrimSpace(input.AgentClass)
	if agentClass != "" && agentClass != "worker" && agentClass != "orchestrator" {
		return nil, newError(CodeValidation, "agent_class must be worker or orchestrator", false, 0)
	}
	mutationClass := strings.TrimSpace(input.MutationClass)
	if mutationClass != "" && mutationClass != "observe" && mutationClass != "recommend" && mutationClass != "mutate" {
		return nil, newError(CodeValidation, "mutation_class must be observe, recommend, or mutate", false, 0)
	}
	ttl := input.TTLSeconds
	if ttl <= 0 {
		ttl = int(s.cfg.DefaultRegistrationTTL.Seconds())
	}
	build := normalizeBuildInfo(input.Build)
	meta := normalizeAgentMeta(input.Meta)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	agent := &Agent{
		AgentID:       agentID,
		Capabilities:  cloneStrings(input.Capabilities),
		Version:       strings.TrimSpace(input.Version),
		Description:   strings.TrimSpace(input.Description),
		AgentClass:    agentClass,
		MutationClass: mutationClass,
		Build:         build,
		Meta:          meta,
		Mode:          mode,
		CallbackURL:   strings.TrimSpace(input.CallbackURL),
		Status:        AgentStatusActive,
		RegisteredAt:  now,
		ExpiresAt:     now.Add(time.Duration(ttl) * time.Second),
		TTLSeconds:    ttl,
	}
	if existing, ok := s.agents[agentID]; ok {
		agent.RegisteredAt = existing.RegisteredAt
	}
	s.agents[agentID] = agent
	if _, ok := s.inboxes[agentID]; !ok {
		s.inboxes[agentID] = []InboxEvent{}
		s.inboxBase[agentID] = 0
	}

	s.publishLocked(
		ObserveAgentRegistered,
		map[string]any{
			"agent_id":     agent.AgentID,
			"capabilities": agent.Capabilities,
			"at":           now,
		},
		"",
		[]string{agent.AgentID},
		now,
	)

	cp := cloneAgent(agent)
	return &cp, nil
}

func (s *Store) ListAgents(capability string) []Agent {
	now := s.now()
	capability = strings.TrimSpace(capability)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	out := []Agent{}
	for _, a := range s.agents {
		if a.Status != AgentStatusActive {
			continue
		}
		if capability != "" {
			found := false
			for _, c := range a.Capabilities {
				if c == capability {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, cloneAgent(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

func (s *Store) CreateConversation(input CreateConversationInput) (*Conversation, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	c := s.ensureConversationLocked(input, now)
	cp := *c
	return &cp, nil
}

func (s *Store) ListConversations(filter ListConversationsFilter) []Conversation {
	now := s.now()
	participant := strings.TrimSpace(filter.Participant)
	status := strings.TrimSpace(filter.Status)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	out := []Conversation{}
	for _, c := range s.conversations {
		if status != "" && c.Status != status {
			continue
		}
		if participant != "" {
			found := false
			for _, p := range c.Participants {
				if p == participant {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		cp := *c
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) SendMessage(input SendMessageInput) (*Message, bool, error) {
	now := s.now()
	to := strings.TrimSpace(input.To)
	from := strings.TrimSpace(input.From)
	requestID := strings.TrimSpace(input.RequestID)
	body := strings.TrimSpace(input.Body)
	if to == "" {
		return nil, false, newError(CodeValidation, "to is required", false, 0)
	}
	if from == "" {
		return nil, false, newError(CodeValidation, "from is required", false, 0)
	}
	if requestID == "" {
		return nil, false, newError(CodeValidation, "request_id is required", false, 0)
	}
	if body == "" {
		return nil, false, newError(CodeValidation, "body is required", false, 0)
	}
	msgType := input.Type
	if msgType == "" {
		msgType = MessageTypeRequest
	}
	if msgType != MessageTypeRequest && msgType != MessageTypeResponse && msgType != MessageTypeInform {
		return nil, false, newError(CodeValidation, "type must be request, response, or inform", false, 0)
	}

	ttl := input.TTLSeconds
	if ttl <= 0 {
		ttl = int(s.cfg.DefaultMessageTTL.Seconds())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	sender, ok := s.agents[from]
	if !ok || sender.Status != AgentStatusActive {
		return nil, false, newError(CodeUnauthorized, "sender is not registered/active", false, 0)
	}

	target, ok := s.agents[to]
	if !ok {
		return nil, false, newError(CodeNotFound, "target agent not registered", false, 0)
	}

	key := dedupeKey(from, to, requestID)
	if dedup, ok := s.idempotency[key]; ok && now.Sub(dedup.CreatedAt) <= s.cfg.IdempotencyWindow {
		if existing, ok := s.messages[dedup.MessageID]; ok {
			cp := *existing
			return &cp, true, nil
		}
	}

	conv := s.ensureConversationLocked(CreateConversationInput{
		ConversationID: input.ConversationID,
		Participants:   []string{from, to},
	}, now)

	s.nextMessageID++
	mid := fmt.Sprintf("m-%06d", s.nextMessageID)
	m := &Message{
		MessageID:      mid,
		Type:           msgType,
		From:           from,
		To:             to,
		ConversationID: conv.ConversationID,
		RequestID:      requestID,
		InReplyTo:      strings.TrimSpace(input.InReplyTo),
		Body:           body,
		Meta:           input.Meta,
		Attachments:    append([]Attachment{}, input.Attachments...),
		State:          StatePending,
		CreatedAt:      now,
		TTLExpiresAt:   now.Add(time.Duration(ttl) * time.Second),
	}

	if msgType != MessageTypeRequest {
		m.State = StateCompleted
	}

	pushCallbackURL := ""
	pushPayload := map[string]any(nil)

	if target.Status == AgentStatusExpired {
		graceUntil := target.ExpiresAt.Add(s.cfg.GracePeriod)
		if now.After(graceUntil) {
			return nil, false, newError(CodeNotFound, "target agent expired beyond grace period", false, 0)
		}
		m.QueuedForAgent = true
		m.GraceUntil = graceUntil
	} else if msgType == MessageTypeRequest {
		m.State = StateWaitingAck
		m.DeliveredAt = now
		if target.Mode == AgentModePush && strings.TrimSpace(target.CallbackURL) != "" {
			pushCallbackURL = strings.TrimSpace(target.CallbackURL)
			pushPayload = map[string]any{
				"message_id":      m.MessageID,
				"type":            m.Type,
				"from":            m.From,
				"conversation_id": m.ConversationID,
				"body":            m.Body,
				"meta":            m.Meta,
				"attachments":     m.Attachments,
				"created_at":      m.CreatedAt,
			}
		}
		s.appendInboxLocked(to, InboxEvent{
			MessageID:      m.MessageID,
			Type:           m.Type,
			From:           m.From,
			ConversationID: m.ConversationID,
			Body:           m.Body,
			Meta:           m.Meta,
			Attachments:    append([]Attachment{}, m.Attachments...),
			CreatedAt:      m.CreatedAt,
		})
	} else {
		if target.Mode == AgentModePush && strings.TrimSpace(target.CallbackURL) != "" {
			pushCallbackURL = strings.TrimSpace(target.CallbackURL)
			pushPayload = map[string]any{
				"message_id":      m.MessageID,
				"type":            m.Type,
				"from":            m.From,
				"conversation_id": m.ConversationID,
				"body":            m.Body,
				"meta":            m.Meta,
				"attachments":     m.Attachments,
				"created_at":      m.CreatedAt,
			}
		}
		s.appendInboxLocked(to, InboxEvent{
			MessageID:      m.MessageID,
			Type:           m.Type,
			From:           m.From,
			ConversationID: m.ConversationID,
			Body:           m.Body,
			Meta:           m.Meta,
			Attachments:    append([]Attachment{}, m.Attachments...),
			CreatedAt:      m.CreatedAt,
		})
	}

	s.messages[mid] = m
	s.conversationMessages[conv.ConversationID] = append(s.conversationMessages[conv.ConversationID], mid)
	conv.MessageCount = len(s.conversationMessages[conv.ConversationID])
	conv.LastMessageAt = now
	if conv.Status == "" {
		conv.Status = "active"
	}
	s.idempotency[key] = idempotencyEntry{MessageID: mid, CreatedAt: now}

	s.publishLocked(
		ObserveMessage,
		map[string]any{
			"message_id":      m.MessageID,
			"type":            m.Type,
			"from":            m.From,
			"to":              m.To,
			"conversation_id": m.ConversationID,
			"body":            m.Body,
			"created_at":      m.CreatedAt,
		},
		m.ConversationID,
		[]string{m.From, m.To},
		now,
	)

	if pushCallbackURL != "" && pushPayload != nil {
		go s.sendPushCallback(pushCallbackURL, pushPayload)
	}

	cp := *m
	return &cp, false, nil
}

func (s *Store) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, 0, newError(CodeValidation, "agent_id is required", false, 0)
	}

	wait := input.Wait
	if wait < 0 {
		wait = 0
	}
	if wait > s.cfg.InboxWaitMax {
		wait = s.cfg.InboxWaitMax
	}

	deadline := s.now().Add(wait)
	for {
		now := s.now()
		s.mu.Lock()
		s.sweepLocked(now)

		agent, ok := s.agents[agentID]
		if !ok || agent.Status != AgentStatusActive {
			s.mu.Unlock()
			return nil, 0, newError(CodeUnauthorized, "agent is not registered/active", false, 0)
		}

		events := s.inboxes[agentID]
		base := s.inboxBase[agentID]
		cursor := input.Cursor
		if cursor < base {
			cursor = base
		}
		end := base + len(events)
		if cursor > end {
			cursor = end
		}
		if cursor < end {
			start := cursor - base
			out := append([]InboxEvent{}, events[start:]...)
			next := end
			s.mu.Unlock()
			return out, next, nil
		}
		s.mu.Unlock()

		if wait == 0 || s.now().After(deadline) {
			return []InboxEvent{}, cursor, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Store) Ack(input AckInput) error {
	now := s.now()
	agentID := strings.TrimSpace(input.AgentID)
	messageID := strings.TrimSpace(input.MessageID)
	status := strings.TrimSpace(input.Status)
	if agentID == "" || messageID == "" {
		return newError(CodeValidation, "agent_id and message_id are required", false, 0)
	}
	if status != "accepted" && status != "rejected" {
		return newError(CodeValidation, "status must be accepted or rejected", false, 0)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	m, ok := s.messages[messageID]
	if !ok {
		return newError(CodeNotFound, "message not found", false, 0)
	}
	if m.Type != MessageTypeRequest {
		return newError(CodeValidation, "acks only apply to request messages", false, 0)
	}
	if m.To != agentID {
		return newError(CodeUnauthorized, "agent_id does not own this message", false, 0)
	}
	if isTerminal(m.State) {
		return nil
	}

	s.publishLocked(
		ObserveAck,
		map[string]any{
			"message_id": m.MessageID,
			"agent_id":   agentID,
			"status":     status,
			"at":         now,
		},
		m.ConversationID,
		[]string{m.From, m.To},
		now,
	)

	from := m.State
	if status == "rejected" {
		m.State = StateRejected
	} else {
		m.State = StateExecuting
	}
	s.publishLocked(
		ObserveStateChange,
		map[string]any{
			"message_id": m.MessageID,
			"from_state": from,
			"to_state":   m.State,
			"at":         now,
		},
		m.ConversationID,
		[]string{m.From, m.To},
		now,
	)
	return nil
}

func (s *Store) PostEvent(input EventInput) error {
	now := s.now()
	actor := strings.TrimSpace(input.ActorAgentID)
	messageID := strings.TrimSpace(input.MessageID)
	typeRaw := strings.TrimSpace(input.Type)
	body := strings.TrimSpace(input.Body)
	if actor == "" {
		return newError(CodeUnauthorized, "X-Agent-ID is required", false, 0)
	}
	if messageID == "" || typeRaw == "" || body == "" {
		return newError(CodeValidation, "message_id, type, and body are required", false, 0)
	}
	if typeRaw != "progress" && typeRaw != "final" && typeRaw != "error" {
		return newError(CodeValidation, "type must be progress, final, or error", false, 0)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	m, ok := s.messages[messageID]
	if !ok {
		return newError(CodeNotFound, "message not found", false, 0)
	}
	if m.To != actor {
		return newError(CodeUnauthorized, "actor does not own this message", false, 0)
	}
	if m.Type != MessageTypeRequest {
		return newError(CodeValidation, "events only apply to request messages", false, 0)
	}
	if isTerminal(m.State) {
		return nil
	}

	switch typeRaw {
	case "progress":
		if !m.LastProgressAt.IsZero() {
			elapsed := now.Sub(m.LastProgressAt)
			if elapsed < s.cfg.ProgressMinInterval {
				return newError(CodeRateLimited, "progress event too frequent", true, s.cfg.ProgressMinInterval-elapsed)
			}
		}
		if m.State == StateWaitingAck {
			from := m.State
			m.State = StateExecuting
			s.publishLocked(
				ObserveStateChange,
				map[string]any{
					"message_id": m.MessageID,
					"from_state": from,
					"to_state":   m.State,
					"at":         now,
				},
				m.ConversationID,
				[]string{m.From, m.To},
				now,
			)
		}
		m.LastProgressAt = now
		s.publishLocked(
			ObserveProgress,
			map[string]any{
				"message_id": m.MessageID,
				"body":       body,
				"meta":       input.Meta,
				"at":         now,
			},
			m.ConversationID,
			[]string{m.From, m.To},
			now,
		)
	case "final":
		from := m.State
		m.State = StateCompleted
		s.publishLocked(
			ObserveStateChange,
			map[string]any{
				"message_id": m.MessageID,
				"from_state": from,
				"to_state":   m.State,
				"at":         now,
				"body":       body,
				"meta":       input.Meta,
			},
			m.ConversationID,
			[]string{m.From, m.To},
			now,
		)
	case "error":
		from := m.State
		m.State = StateError
		s.publishLocked(
			ObserveStateChange,
			map[string]any{
				"message_id": m.MessageID,
				"from_state": from,
				"to_state":   m.State,
				"at":         now,
				"body":       body,
				"meta":       input.Meta,
			},
			m.ConversationID,
			[]string{m.From, m.To},
			now,
		)
	}

	return nil
}

func (s *Store) Inject(input InjectInput) (*Message, error) {
	now := s.now()
	identity := strings.TrimSpace(input.Identity)
	body := strings.TrimSpace(input.Body)
	to := strings.TrimSpace(input.To)
	if identity == "" || body == "" {
		return nil, newError(CodeValidation, "identity and body are required", false, 0)
	}
	if len(s.humanAllowlist) > 0 {
		if _, ok := s.humanAllowlist[identity]; !ok {
			return nil, newError(CodeUnauthorized, "human identity not allowed", false, 0)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	conv := s.ensureConversationLocked(CreateConversationInput{ConversationID: input.ConversationID}, now)

	s.nextMessageID++
	mid := fmt.Sprintf("m-%06d", s.nextMessageID)
	m := &Message{
		MessageID:      mid,
		Type:           MessageTypeInform,
		From:           "human:" + identity,
		To:             to,
		ConversationID: conv.ConversationID,
		RequestID:      "inject-" + strconv.FormatInt(now.UnixNano(), 10),
		Body:           body,
		Meta:           map[string]any{"identity": identity},
		State:          StateCompleted,
		CreatedAt:      now,
		TTLExpiresAt:   now.Add(s.cfg.DefaultMessageTTL),
	}

	pushCallbackURL := ""
	pushPayload := map[string]any(nil)

	if to != "" {
		target, ok := s.agents[to]
		if !ok {
			return nil, newError(CodeNotFound, "target agent not registered", false, 0)
		}
		if target.Status == AgentStatusExpired {
			graceUntil := target.ExpiresAt.Add(s.cfg.GracePeriod)
			if now.After(graceUntil) {
				return nil, newError(CodeNotFound, "target agent expired beyond grace period", false, 0)
			}
			m.QueuedForAgent = true
			m.GraceUntil = graceUntil
			m.State = StatePending
		} else {
			if target.Mode == AgentModePush && strings.TrimSpace(target.CallbackURL) != "" {
				pushCallbackURL = strings.TrimSpace(target.CallbackURL)
				pushPayload = map[string]any{
					"message_id":      m.MessageID,
					"type":            m.Type,
					"from":            m.From,
					"conversation_id": m.ConversationID,
					"body":            m.Body,
					"meta":            m.Meta,
					"created_at":      m.CreatedAt,
				}
			}
			s.appendInboxLocked(to, InboxEvent{
				MessageID:      m.MessageID,
				Type:           m.Type,
				From:           m.From,
				ConversationID: m.ConversationID,
				Body:           m.Body,
				Meta:           m.Meta,
				CreatedAt:      m.CreatedAt,
			})
		}
	}

	s.messages[mid] = m
	s.conversationMessages[conv.ConversationID] = append(s.conversationMessages[conv.ConversationID], mid)
	conv.MessageCount = len(s.conversationMessages[conv.ConversationID])
	conv.LastMessageAt = now

	s.publishLocked(
		ObserveHumanInjection,
		map[string]any{
			"identity":        identity,
			"message_id":      m.MessageID,
			"conversation_id": m.ConversationID,
			"to":              m.To,
			"body":            m.Body,
			"at":              now,
		},
		m.ConversationID,
		[]string{m.From, m.To},
		now,
	)
	s.publishLocked(
		ObserveMessage,
		map[string]any{
			"message_id":      m.MessageID,
			"type":            m.Type,
			"from":            m.From,
			"to":              m.To,
			"conversation_id": m.ConversationID,
			"body":            m.Body,
			"created_at":      m.CreatedAt,
		},
		m.ConversationID,
		[]string{m.From, m.To},
		now,
	)

	if pushCallbackURL != "" && pushPayload != nil {
		go s.sendPushCallback(pushCallbackURL, pushPayload)
	}

	cp := *m
	return &cp, nil
}

func (s *Store) ListConversationMessages(input ListConversationMessagesInput) (string, []Message, int, error) {
	now := s.now()
	if strings.TrimSpace(input.ConversationID) == "" {
		return "", nil, 0, newError(CodeValidation, "conversation_id is required", false, 0)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	conv, ok := s.conversations[input.ConversationID]
	if !ok {
		return "", nil, 0, newError(CodeNotFound, "conversation not found", false, 0)
	}
	ids := s.conversationMessages[conv.ConversationID]
	cursor := input.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(ids) {
		cursor = len(ids)
	}
	end := cursor + limit
	if end > len(ids) {
		end = len(ids)
	}

	out := make([]Message, 0, end-cursor)
	for _, id := range ids[cursor:end] {
		if m, ok := s.messages[id]; ok {
			cp := *m
			out = append(out, cp)
		}
	}
	return conv.ConversationID, out, end, nil
}

func eventMatchesFilter(evt ObserveEvent, filter ObserveFilter) bool {
	if filter.ConversationID != "" && evt.ConversationID != filter.ConversationID {
		return false
	}
	if filter.AgentID != "" {
		for _, id := range evt.AgentIDs {
			if id == filter.AgentID {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Store) ObserveSince(afterID int64, filter ObserveFilter, wait time.Duration) ([]ObserveEvent, int64) {
	if wait < 0 {
		wait = 0
	}
	if wait > s.cfg.InboxWaitMax {
		wait = s.cfg.InboxWaitMax
	}
	deadline := s.now().Add(wait)

	for {
		now := s.now()
		s.mu.Lock()
		s.sweepLocked(now)
		out := []ObserveEvent{}
		last := afterID
		for _, evt := range s.observeEvents {
			if evt.ID <= afterID {
				continue
			}
			if !eventMatchesFilter(evt, filter) {
				continue
			}
			out = append(out, evt)
			last = evt.ID
		}
		s.mu.Unlock()

		if len(out) > 0 || wait == 0 || s.now().After(deadline) {
			return out, last
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Store) Health() map[string]any {
	counts := s.snapshotCounts()
	return map[string]any{
		"ok":      true,
		"status":  "healthy",
		"agents":  counts.active,
		"observe": counts.observeEvents,
		"push": map[string]any{
			"successes": counts.pushSuccesses,
			"failures":  counts.pushFailures,
		},
	}
}

func (s *Store) SystemStatus() map[string]any {
	counts := s.snapshotCounts()
	return map[string]any{
		"ok": true,
		"system": map[string]any{
			"agents_active":  counts.active,
			"agents_expired": counts.expired,
			"conversations":  counts.conversations,
			"messages":       counts.messages,
			"observe_events": counts.observeEvents,
			"push_successes": counts.pushSuccesses,
			"push_failures":  counts.pushFailures,
		},
	}
}

func (s *Store) Metrics() string {
	counts := s.snapshotCounts()
	var b strings.Builder
	fmt.Fprintln(&b, "# HELP agent_bus_agents_active Number of active registered agents.")
	fmt.Fprintln(&b, "# TYPE agent_bus_agents_active gauge")
	fmt.Fprintf(&b, "agent_bus_agents_active %d\n", counts.active)
	fmt.Fprintln(&b, "# HELP agent_bus_agents_expired Number of expired agents still tracked.")
	fmt.Fprintln(&b, "# TYPE agent_bus_agents_expired gauge")
	fmt.Fprintf(&b, "agent_bus_agents_expired %d\n", counts.expired)
	fmt.Fprintln(&b, "# HELP agent_bus_conversations_total Number of tracked conversations.")
	fmt.Fprintln(&b, "# TYPE agent_bus_conversations_total gauge")
	fmt.Fprintf(&b, "agent_bus_conversations_total %d\n", counts.conversations)
	fmt.Fprintln(&b, "# HELP agent_bus_messages_total Number of tracked messages.")
	fmt.Fprintln(&b, "# TYPE agent_bus_messages_total gauge")
	fmt.Fprintf(&b, "agent_bus_messages_total %d\n", counts.messages)
	fmt.Fprintln(&b, "# HELP agent_bus_observe_events_total Number of retained observe events.")
	fmt.Fprintln(&b, "# TYPE agent_bus_observe_events_total gauge")
	fmt.Fprintf(&b, "agent_bus_observe_events_total %d\n", counts.observeEvents)
	fmt.Fprintln(&b, "# HELP agent_bus_push_successes_total Successful push callback deliveries.")
	fmt.Fprintln(&b, "# TYPE agent_bus_push_successes_total counter")
	fmt.Fprintf(&b, "agent_bus_push_successes_total %d\n", counts.pushSuccesses)
	fmt.Fprintln(&b, "# HELP agent_bus_push_failures_total Failed push callback deliveries after retries.")
	fmt.Fprintln(&b, "# TYPE agent_bus_push_failures_total counter")
	fmt.Fprintf(&b, "agent_bus_push_failures_total %d\n", counts.pushFailures)
	return b.String()
}

func (s *Store) snapshotCounts() snapshotCounts {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	counts := snapshotCounts{
		conversations: len(s.conversations),
		messages:      len(s.messages),
		observeEvents: len(s.observeEvents),
		pushSuccesses: s.pushSuccesses,
		pushFailures:  s.pushFailures,
	}
	for _, a := range s.agents {
		if a.Status == AgentStatusActive {
			counts.active++
		} else {
			counts.expired++
		}
	}
	return counts
}

func (s *Store) GetMessageForTest(messageID string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[messageID]
	if !ok {
		return Message{}, false
	}
	cp := *m
	return cp, true
}
