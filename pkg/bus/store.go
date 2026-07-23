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
	"sync/atomic"
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
	// PushQueueSize bounds the buffered queue of pending push callbacks.
	// When the queue is full new deliveries are dropped (logged and counted
	// as push failures) instead of blocking the API path or spawning
	// per-job goroutines. Defaults to 256.
	PushQueueSize int
	// PushWorkers is the fixed number of goroutines draining the push
	// queue. Defaults to 4.
	PushWorkers            int
	MaxInboxEventsPerAgent int
	MaxObserveEvents       int
	// MaxInboxBytesPerAgent bounds the approximate retained payload bytes per
	// agent inbox; oldest events are evicted first. Counts alone do not bound
	// memory when individual bodies are large. Defaults to 32 MiB; negative
	// disables.
	MaxInboxBytesPerAgent int
	// MaxObserveBytes bounds the approximate retained payload bytes of the
	// observe ring. Defaults to 64 MiB; negative disables.
	MaxObserveBytes int
	// MessageRetention prunes terminal (completed/rejected/error) messages
	// this long after they reached their terminal state. Defaults to 1h;
	// negative disables.
	MessageRetention time.Duration
	// MessageMaxAge prunes any message, regardless of state, this long after
	// creation. Backstop for stuck or legacy-loaded messages. Defaults to 24h;
	// negative disables.
	MessageMaxAge time.Duration
	// ConversationRetention prunes conversations idle this long once all their
	// messages have been pruned. Defaults to 24h; negative disables.
	ConversationRetention time.Duration
	// AgentRetention prunes expired agents (and their inboxes) this long after
	// expiry. Returning agents simply re-register. Defaults to 24h; negative
	// disables.
	AgentRetention time.Duration
	// SweepMinInterval bounds how often the expensive full-state sweep runs.
	// Successive sweep calls inside this window are skipped, so idle hot paths
	// (PollInbox / ObserveSince / Health / Metrics) stay cheap on stores
	// holding hundreds of thousands of retained messages. Defaults to 250ms.
	SweepMinInterval time.Duration
	Clock            func() time.Time
	Logger           *log.Logger
	// NamespaceMode controls the migration window for namespace-prefixed IDs.
	// "compat" accepts legacy unprefixed IDs and assigns them LegacyScope.
	// "strict" rejects unprefixed IDs. Defaults to compat.
	NamespaceMode NamespaceMode
	// LegacyScope is the server-authoritative scope assigned to unprefixed
	// IDs while NamespaceMode is compat. Defaults to ucla.
	LegacyScope Scope
	// SharedGrantAgents is the server-authoritative list of identities that
	// may access shared.* resources. Registration bodies may request grants,
	// but only this policy takes effect.
	SharedGrantAgents []string
}

type idempotencyEntry struct {
	MessageID string
	CreatedAt time.Time
}

// pushJob is one push-callback delivery handed to the worker pool.
type pushJob struct {
	url     string
	payload map[string]any
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
	inboxBytes           map[string]int
	observeEvents        []ObserveEvent
	observeBytes         int
	idempotency          map[string]idempotencyEntry

	lastSweepAt time.Time

	// inboxNotify holds a per-agent broadcast channel that the inbox waiters
	// receive on. The channel is closed (and replaced) whenever new state
	// lands in that agent's inbox, waking any in-flight long-poller without
	// the previous 100ms sleep loop.
	inboxNotify map[string]chan struct{}
	// observeNotify is the global broadcast channel for observe waiters;
	// closed (and replaced) whenever a new observe event is published.
	observeNotify chan struct{}

	// sweepSkipped counts sweep invocations that returned early due to
	// SweepMinInterval. Used by tests/benchmarks to assert idle CPU savings.
	sweepSkipped atomic.Uint64
	// sweepRan counts sweep invocations that executed the full body.
	sweepRan atomic.Uint64

	humanAllowlist map[string]struct{}
	httpClient     *http.Client
	logger         *log.Logger
	pushFailures   int64
	pushSuccesses  int64

	// pushQueue feeds the fixed pool of push workers. Bounded so a burst of
	// sends to a dead push agent cannot pile up unbounded goroutines.
	pushQueue chan pushJob
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
	if cfg.PushQueueSize <= 0 {
		cfg.PushQueueSize = 256
	}
	if cfg.PushWorkers <= 0 {
		cfg.PushWorkers = 4
	}
	if cfg.MaxInboxEventsPerAgent <= 0 {
		cfg.MaxInboxEventsPerAgent = 10000
	}
	if cfg.MaxObserveEvents <= 0 {
		cfg.MaxObserveEvents = 50000
	}
	if cfg.MaxInboxBytesPerAgent == 0 {
		cfg.MaxInboxBytesPerAgent = 32 << 20
	}
	if cfg.MaxInboxBytesPerAgent < 0 {
		cfg.MaxInboxBytesPerAgent = 0
	}
	if cfg.MaxObserveBytes == 0 {
		cfg.MaxObserveBytes = 64 << 20
	}
	if cfg.MaxObserveBytes < 0 {
		cfg.MaxObserveBytes = 0
	}
	if cfg.MessageRetention == 0 {
		cfg.MessageRetention = time.Hour
	}
	if cfg.MessageRetention < 0 {
		cfg.MessageRetention = 0
	}
	if cfg.MessageMaxAge == 0 {
		cfg.MessageMaxAge = 24 * time.Hour
	}
	if cfg.MessageMaxAge < 0 {
		cfg.MessageMaxAge = 0
	}
	if cfg.ConversationRetention == 0 {
		cfg.ConversationRetention = 24 * time.Hour
	}
	if cfg.ConversationRetention < 0 {
		cfg.ConversationRetention = 0
	}
	if cfg.AgentRetention == 0 {
		cfg.AgentRetention = 24 * time.Hour
	}
	if cfg.AgentRetention < 0 {
		cfg.AgentRetention = 0
	}
	if cfg.SweepMinInterval < 0 {
		cfg.SweepMinInterval = 0
	}
	if cfg.SweepMinInterval == 0 {
		cfg.SweepMinInterval = 250 * time.Millisecond
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.NamespaceMode == "" {
		cfg.NamespaceMode = NamespaceModeCompat
	}
	if cfg.NamespaceMode != NamespaceModeCompat && cfg.NamespaceMode != NamespaceModeStrict {
		cfg.NamespaceMode = NamespaceModeCompat
	}
	if cfg.LegacyScope == "" {
		cfg.LegacyScope = ScopeUCLA
	}
	if _, ok := validScopes[cfg.LegacyScope]; !ok || cfg.LegacyScope == ScopeShared {
		cfg.LegacyScope = ScopeUCLA
	}

	allowlist := map[string]struct{}{}
	for _, raw := range strings.Split(os.Getenv("HUMAN_ALLOWLIST"), ",") {
		v := strings.TrimSpace(raw)
		if v != "" {
			allowlist[v] = struct{}{}
		}
	}

	s := &Store{
		cfg:                  cfg,
		agents:               map[string]*Agent{},
		conversations:        map[string]*Conversation{},
		messages:             map[string]*Message{},
		conversationMessages: map[string][]string{},
		inboxes:              map[string][]InboxEvent{},
		inboxBase:            map[string]int{},
		inboxBytes:           map[string]int{},
		observeEvents:        []ObserveEvent{},
		idempotency:          map[string]idempotencyEntry{},
		inboxNotify:          map[string]chan struct{}{},
		observeNotify:        make(chan struct{}),
		humanAllowlist:       allowlist,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:    cfg.Logger,
		pushQueue: make(chan pushJob, cfg.PushQueueSize),
	}
	if s.logger == nil {
		s.logger = log.New(os.Stdout, "pinakes ", log.LstdFlags)
	}

	// Push workers are daemon goroutines for the life of the process. Store
	// has no Close, so they are never shut down; that is accepted — they are
	// a fixed pool, not per-job spawns.
	for i := 0; i < cfg.PushWorkers; i++ {
		go func() {
			for job := range s.pushQueue {
				s.sendPushCallback(job.url, job.payload)
			}
		}()
	}

	return s
}

func (s *Store) now() time.Time {
	return s.cfg.Clock().UTC()
}

func dedupeKey(from, to, requestID string) string {
	return from + "\x1f" + to + "\x1f" + requestID
}

func (s *Store) logScopeDenied(action, identity, resource, reason string) {
	if s.logger == nil {
		return
	}
	s.logger.Printf("WARN scope denied action=%s identity=%s resource=%s reason=%s", action, identity, resource, reason)
}

func (s *Store) authorizeAgentForName(agent *Agent, action, resource string) error {
	scope, ok := s.scopeOfName(resource)
	if !ok {
		s.logScopeDenied(action, agent.AgentID, resource, "unprefixed resource")
		return newError(CodeValidation, "topic/queue name must be prefixed with personal., ucla., or shared.", false, 0)
	}
	if scope == ScopeShared {
		if s.agentHasSharedGrant(agent.AgentID) {
			return nil
		}
		s.logScopeDenied(action, agent.AgentID, resource, "shared grant required")
		return newError(CodeUnauthorized, "shared.* access requires explicit shared grant", false, 0)
	}
	if s.agentHasScope(agent.AgentID, scope) {
		return nil
	}
	s.logScopeDenied(action, agent.AgentID, resource, "scope not allowed")
	return newError(CodeUnauthorized, "identity is not allowed to access this scope", false, 0)
}

func (s *Store) agentCanAccessName(agentID, resource string) bool {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return true
	}
	scope, ok := s.scopeOfName(resource)
	if !ok {
		return false
	}
	if scope == ScopeShared {
		return s.agentHasSharedGrant(agentID)
	}
	return s.agentHasScope(agentID, scope)
}

func (s *Store) actorCanAccessAnyName(actor string, names []string) bool {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return true
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if s.agentCanAccessName(actor, name) {
			return true
		}
	}
	return false
}

func (s *Store) actorCanAccessConversation(actor string, conv *Conversation) bool {
	if conv == nil {
		return false
	}
	return s.actorCanAccessAnyName(actor, conv.Participants)
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

// inboxEventSize approximates the retained bytes of an inbox event. Meta is
// marshaled because it is opaque; this only runs on append and trim.
func inboxEventSize(evt InboxEvent) int {
	size := 64 + len(evt.MessageID) + len(evt.From) + len(evt.ConversationID) + len(evt.Body)
	for _, a := range evt.Attachments {
		size += 32 + len(a.URL) + len(a.Name) + len(a.ContentType) + len(a.SHA256)
	}
	if evt.Meta != nil {
		if blob, err := json.Marshal(evt.Meta); err == nil {
			size += len(blob)
		}
	}
	return size
}

func (s *Store) appendInboxLocked(agentID string, evt InboxEvent) {
	s.inboxes[agentID] = append(s.inboxes[agentID], evt)
	s.inboxBytes[agentID] += inboxEventSize(evt)
	events := s.inboxes[agentID]

	drop := 0
	if max := s.cfg.MaxInboxEventsPerAgent; max > 0 && len(events) > max {
		drop = len(events) - max
	}
	// Byte budget: evict oldest first, but never the event just appended —
	// the sender already got a success response for it.
	if maxBytes := s.cfg.MaxInboxBytesPerAgent; maxBytes > 0 {
		remaining := s.inboxBytes[agentID]
		for i := 0; i < drop; i++ {
			remaining -= inboxEventSize(events[i])
		}
		for drop < len(events)-1 && remaining > maxBytes {
			remaining -= inboxEventSize(events[drop])
			drop++
		}
	}
	if drop > 0 {
		s.dropInboxPrefixLocked(agentID, drop)
	}
	s.signalInboxLocked(agentID)
}

// dropInboxPrefixLocked removes the oldest n events from an agent inbox,
// advancing the base cursor and byte accounting.
func (s *Store) dropInboxPrefixLocked(agentID string, n int) {
	events := s.inboxes[agentID]
	if n <= 0 || n > len(events) {
		return
	}
	for i := 0; i < n; i++ {
		s.inboxBytes[agentID] -= inboxEventSize(events[i])
	}
	s.inboxes[agentID] = append([]InboxEvent{}, events[n:]...)
	s.inboxBase[agentID] += n
}

// inboxNotifyChanLocked returns the broadcast channel for an agent inbox.
// Callers hold s.mu and read the returned channel after releasing it.
func (s *Store) inboxNotifyChanLocked(agentID string) chan struct{} {
	ch, ok := s.inboxNotify[agentID]
	if !ok || ch == nil {
		ch = make(chan struct{})
		s.inboxNotify[agentID] = ch
	}
	return ch
}

// signalInboxLocked wakes inbox waiters by closing the broadcast channel
// (if any) and clearing the slot so future waiters allocate a fresh one.
func (s *Store) signalInboxLocked(agentID string) {
	if ch, ok := s.inboxNotify[agentID]; ok && ch != nil {
		close(ch)
		delete(s.inboxNotify, agentID)
	}
}

// signalObserveLocked wakes observe waiters by closing the current channel
// and installing a new one. Always non-nil so waiters can read it safely.
func (s *Store) signalObserveLocked() {
	close(s.observeNotify)
	s.observeNotify = make(chan struct{})
}

// observeEventSize approximates the retained bytes of an observe event.
func observeEventSize(evt ObserveEvent) int {
	size := 64 + len(evt.ConversationID)
	for _, id := range evt.AgentIDs {
		size += len(id)
	}
	if evt.Data != nil {
		if blob, err := json.Marshal(evt.Data); err == nil {
			size += len(blob)
		}
	}
	return size
}

func (s *Store) trimObserveLocked() {
	events := s.observeEvents
	drop := 0
	if max := s.cfg.MaxObserveEvents; max > 0 && len(events) > max {
		drop = len(events) - max
	}
	// Byte budget: evict oldest first, but always keep the newest event.
	if maxBytes := s.cfg.MaxObserveBytes; maxBytes > 0 {
		remaining := s.observeBytes
		for i := 0; i < drop; i++ {
			remaining -= events[i].Size
		}
		for drop < len(events)-1 && remaining > maxBytes {
			remaining -= events[drop].Size
			drop++
		}
	}
	if drop == 0 {
		return
	}
	for i := 0; i < drop; i++ {
		s.observeBytes -= events[i].Size
	}
	s.observeEvents = append([]ObserveEvent{}, events[drop:]...)
}

// enqueuePushLocked hands a push delivery to the worker pool without ever
// blocking the API path. On a full queue the job is dropped, logged, and
// counted as a push failure. Caller must hold s.mu (both call sites enqueue
// inside their critical section), which is why the drop path increments
// pushFailures directly instead of re-locking.
func (s *Store) enqueuePushLocked(url string, payload map[string]any) {
	select {
	case s.pushQueue <- pushJob{url: url, payload: payload}:
	default:
		s.logger.Printf("WARN push queue full, dropping delivery url=%s", url)
		s.pushFailures++
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
	evt := ObserveEvent{
		ID:             s.nextObserveID,
		Type:           eventType,
		At:             at,
		Data:           data,
		ConversationID: conversationID,
		AgentIDs:       agentIDs,
	}
	evt.Size = observeEventSize(evt)
	s.observeEvents = append(s.observeEvents, evt)
	s.observeBytes += evt.Size
	s.trimObserveLocked()
	s.signalObserveLocked()
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
	// Rate-limit the expensive full state walk. The bus reaches near-100% CPU
	// once `messages` holds hundreds of thousands of entries because every API
	// call (and every 100ms long-poll tick) used to scan them all. The first
	// sweep after process start still runs because lastSweepAt is zero.
	if s.cfg.SweepMinInterval > 0 && !s.lastSweepAt.IsZero() && now.Sub(s.lastSweepAt) < s.cfg.SweepMinInterval {
		s.sweepSkipped.Add(1)
		return
	}
	s.lastSweepAt = now
	s.sweepRan.Add(1)

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
			m.TerminalAt = now
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
			m.TerminalAt = now
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
				m.TerminalAt = now
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

	s.pruneLocked(now)
}

// pruneLocked reclaims memory: terminal messages past retention, any message
// past the hard age cap, idle conversations whose messages are gone, and
// expired agents (with their inboxes) past retention. Without this the bus
// grows without bound between restarts and gets OOM-killed at the container
// memory cap.
func (s *Store) pruneLocked(now time.Time) {
	if s.cfg.MessageRetention > 0 || s.cfg.MessageMaxAge > 0 {
		for id, m := range s.messages {
			prune := false
			if s.cfg.MessageMaxAge > 0 && now.Sub(m.CreatedAt) > s.cfg.MessageMaxAge {
				prune = true
			} else if s.cfg.MessageRetention > 0 && isTerminal(m.State) {
				terminalAt := m.TerminalAt
				if terminalAt.IsZero() {
					// Legacy state loaded without TerminalAt: fall back to
					// CreatedAt so pre-retention backlogs drain promptly.
					terminalAt = m.CreatedAt
				}
				if now.Sub(terminalAt) > s.cfg.MessageRetention {
					prune = true
				}
			}
			if prune {
				delete(s.messages, id)
			}
		}
	}

	if s.cfg.ConversationRetention > 0 {
		for cid, c := range s.conversations {
			if now.Sub(c.LastMessageAt) <= s.cfg.ConversationRetention {
				continue
			}
			live := false
			for _, mid := range s.conversationMessages[cid] {
				if _, ok := s.messages[mid]; ok {
					live = true
					break
				}
			}
			if live {
				continue
			}
			delete(s.conversations, cid)
			delete(s.conversationMessages, cid)
		}
	}

	if s.cfg.AgentRetention > 0 {
		for id, a := range s.agents {
			if a.Status != AgentStatusExpired || now.Sub(a.ExpiresAt) <= s.cfg.AgentRetention {
				continue
			}
			delete(s.agents, id)
			s.signalInboxLocked(id)
			delete(s.inboxes, id)
			delete(s.inboxBase, id)
			delete(s.inboxBytes, id)
		}
	}
}

func (s *Store) RegisterAgent(input RegisterAgentInput) (*Agent, error) {
	now := s.now()
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, newError(CodeValidation, "agent_id is required", false, 0)
	}
	if _, ok := s.scopeOfName(agentID); !ok {
		return nil, newError(CodeValidation, "agent_id must be prefixed with personal., ucla., or shared.", false, 0)
	}
	if _, err := normalizeScopes(input.AllowedScopes); err != nil {
		return nil, err
	}
	if _, err := normalizeSharedGrants(input.SharedGrants); err != nil {
		return nil, err
	}
	allowedScopes := s.agentAllowedScopes(agentID)
	sharedGrants := s.agentSharedGrants(agentID)
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
		AllowedScopes: allowedScopes,
		SharedGrants:  sharedGrants,
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
	if err := s.authorizeAgentForName(agent, "subscribe", agentID); err != nil {
		return nil, err
	}
	s.agents[agentID] = agent
	if _, ok := s.inboxes[agentID]; !ok {
		s.inboxes[agentID] = []InboxEvent{}
		s.inboxBase[agentID] = 0
		s.inboxBytes[agentID] = 0
	}

	s.publishLocked(
		ObserveAgentRegistered,
		map[string]any{
			"agent_id":       agent.AgentID,
			"allowed_scopes": agent.AllowedScopes,
			"shared_grants":  agent.SharedGrants,
			"capabilities":   agent.Capabilities,
			"at":             now,
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
	for _, participant := range input.Participants {
		if strings.TrimSpace(participant) == "" {
			continue
		}
		if _, ok := s.scopeOfName(participant); !ok {
			return nil, newError(CodeValidation, "participants must be prefixed with personal., ucla., or shared.", false, 0)
		}
	}
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
	actor := strings.TrimSpace(filter.ActorAgentID)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	out := []Conversation{}
	for _, c := range s.conversations {
		if status != "" && c.Status != status {
			continue
		}
		if !s.actorCanAccessConversation(actor, c) {
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
	if _, ok := s.scopeOfName(to); !ok {
		return nil, false, newError(CodeValidation, "to must be prefixed with personal., ucla., or shared.", false, 0)
	}
	if from == "" {
		return nil, false, newError(CodeValidation, "from is required", false, 0)
	}
	if _, ok := s.scopeOfName(from); !ok {
		return nil, false, newError(CodeValidation, "from must be prefixed with personal., ucla., or shared.", false, 0)
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
	if err := s.authorizeAgentForName(sender, "publish", from); err != nil {
		return nil, false, err
	}
	if err := s.authorizeAgentForName(sender, "publish", to); err != nil {
		return nil, false, err
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
		m.TerminalAt = now
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
		s.enqueuePushLocked(pushCallbackURL, pushPayload)
	}

	cp := *m
	return &cp, false, nil
}

func (s *Store) PollInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, 0, newError(CodeValidation, "agent_id is required", false, 0)
	}
	if _, ok := s.scopeOfName(agentID); !ok {
		return nil, 0, newError(CodeValidation, "agent_id must be prefixed with personal., ucla., or shared.", false, 0)
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
		if err := s.authorizeAgentForName(agent, "subscribe", agentID); err != nil {
			s.mu.Unlock()
			return nil, 0, err
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
		// Cursor values originate from prior poll responses, so a poll at
		// cursor C proves the agent received everything below C. Reclaim
		// those events instead of holding them until a cap evicts them.
		if cursor > base {
			s.dropInboxPrefixLocked(agentID, cursor-base)
			events = s.inboxes[agentID]
			base = cursor
		}
		if cursor < end {
			start := cursor - base
			out := append([]InboxEvent{}, events[start:]...)
			next := end
			s.mu.Unlock()
			return out, next, nil
		}

		if wait == 0 {
			s.mu.Unlock()
			return []InboxEvent{}, cursor, nil
		}
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			s.mu.Unlock()
			return []InboxEvent{}, cursor, nil
		}
		notifier := s.inboxNotifyChanLocked(agentID)
		s.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-notifier:
			timer.Stop()
		case <-timer.C:
			return []InboxEvent{}, cursor, nil
		}
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
		m.TerminalAt = now
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
		m.TerminalAt = now
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
		m.TerminalAt = now
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
	if to != "" {
		if _, ok := s.scopeOfName(to); !ok {
			return nil, newError(CodeValidation, "to must be prefixed with personal., ucla., or shared.", false, 0)
		}
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

	if isTerminal(m.State) {
		m.TerminalAt = now
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
		s.enqueuePushLocked(pushCallbackURL, pushPayload)
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
	if !s.actorCanAccessConversation(strings.TrimSpace(input.ActorAgentID), conv) {
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

func (s *Store) eventMatchesFilter(evt ObserveEvent, filter ObserveFilter) bool {
	if filter.ConversationID != "" && evt.ConversationID != filter.ConversationID {
		return false
	}
	if filter.AgentID != "" {
		for _, id := range evt.AgentIDs {
			if id == filter.AgentID {
				return s.actorCanAccessAnyName(filter.ActorAgentID, evt.AgentIDs)
			}
		}
		return false
	}
	return s.actorCanAccessAnyName(filter.ActorAgentID, evt.AgentIDs)
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
		// observeEvents are appended in monotonically increasing ID order,
		// so we can binary-search for the first event past afterID instead
		// of scanning all 50k retained entries per call.
		idx := sort.Search(len(s.observeEvents), func(i int) bool {
			return s.observeEvents[i].ID > afterID
		})
		out := []ObserveEvent{}
		last := afterID
		for _, evt := range s.observeEvents[idx:] {
			if !s.eventMatchesFilter(evt, filter) {
				continue
			}
			out = append(out, evt)
			last = evt.ID
		}

		if len(out) > 0 {
			s.mu.Unlock()
			return out, last
		}
		if wait == 0 {
			s.mu.Unlock()
			return out, last
		}
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			s.mu.Unlock()
			return out, last
		}
		notifier := s.observeNotify
		s.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-notifier:
			timer.Stop()
		case <-timer.C:
			return []ObserveEvent{}, last
		}
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
