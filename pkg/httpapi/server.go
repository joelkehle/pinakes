package httpapi

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

type Server struct {
	store bus.API

	mu            sync.RWMutex
	agentSecrets  map[string]string
	agentAllowset map[string]struct{}
}

func NewServer(store bus.API) http.Handler {
	allowset := map[string]struct{}{}
	for _, raw := range strings.Split(os.Getenv("AGENT_ALLOWLIST"), ",") {
		v := strings.TrimSpace(raw)
		if v != "" {
			allowset[v] = struct{}{}
		}
	}

	s := &Server{
		store:         store,
		agentSecrets:  map[string]string{},
		agentAllowset: allowset,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/agents/register", s.handleRegisterAgent)
	mux.HandleFunc("/v1/agents", s.handleListAgents)
	mux.HandleFunc("/v1/conversations", s.handleConversations)
	mux.HandleFunc("/v1/conversations/", s.handleConversationMessages)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/v1/inbox", s.handleInbox)
	mux.HandleFunc("/v1/acks", s.handleAcks)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/observe", s.handleObserve)
	mux.HandleFunc("/v1/inject", s.handleInject)
	mux.HandleFunc("/health", s.handleTopLevelHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/system/status", s.handleSystemStatus)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeBusError(w http.ResponseWriter, err error) {
	var be *bus.Error
	if errors.As(err, &be) {
		payload := map[string]any{
			"ok": false,
			"error": map[string]any{
				"code":      be.Code,
				"message":   be.Message,
				"transient": be.Transient,
			},
		}
		if be.RetryAfter > 0 {
			payload["error"].(map[string]any)["retry_after"] = be.RetryAfter
			w.Header().Set("Retry-After", strconv.Itoa(be.RetryAfter))
		}
		writeJSON(w, be.Status, payload)
		return
	}
	writeJSON(w, 500, map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":      bus.CodeInternal,
			"message":   err.Error(),
			"transient": true,
		},
	})
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte("{}"), nil
	}
	blob, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(blob) == 0 {
		blob = []byte("{}")
	}
	return blob, nil
}

func decodeJSONBytes(blob []byte, dst any) error {
	return json.Unmarshal(blob, dst)
}

func parseInt(value string, def int) int {
	if strings.TrimSpace(value) == "" {
		return def
	}
	v, err := strconv.Atoi(value)
	if err != nil {
		return def
	}
	return v
}

func parseWaitSeconds(value string) time.Duration {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	v, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	if v < 0 {
		v = 0
	}
	return time.Duration(v) * time.Second
}

func methodOnly(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func (s *Server) verifySignature(agentID, signature string, payload []byte) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return &bus.Error{Code: bus.CodeUnauthorized, Message: "agent id required", Status: 401}
	}
	s.mu.RLock()
	secret, ok := s.agentSecrets[agentID]
	s.mu.RUnlock()
	if !ok || strings.TrimSpace(secret) == "" {
		return &bus.Error{Code: bus.CodeUnauthorized, Message: "agent secret not registered", Status: 401}
	}
	if strings.TrimSpace(signature) == "" {
		return &bus.Error{Code: bus.CodeUnauthorized, Message: "X-Bus-Signature required", Status: 401}
	}

	sig := strings.TrimSpace(signature)
	if strings.HasPrefix(strings.ToLower(sig), "sha256=") {
		sig = sig[len("sha256="):]
	}
	provided, decErr := hex.DecodeString(strings.ToLower(sig))
	if decErr != nil {
		return &bus.Error{Code: bus.CodeUnauthorized, Message: "invalid signature encoding", Status: 401}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, provided) {
		return &bus.Error{Code: bus.CodeUnauthorized, Message: "invalid signature", Status: 401}
	}
	return nil
}

func (s *Server) setAgentSecret(agentID, secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSecrets[agentID] = secret
}

func (s *Server) isAgentAllowed(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.agentAllowset) == 0 {
		return true
	}
	_, ok := s.agentAllowset[agentID]
	return ok
}

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	blob, err := readBody(r)
	if err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	var req struct {
		AgentID      string   `json:"agent_id"`
		Capabilities []string `json:"capabilities"`
		Description  string   `json:"description"`
		Mode         string   `json:"mode"`
		CallbackURL  string   `json:"callback_url"`
		TTL          int      `json:"ttl"`
		Secret       string   `json:"secret"`
	}
	if err := decodeJSONBytes(blob, &req); err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if !s.isAgentAllowed(req.AgentID) {
		writeBusError(w, &bus.Error{Code: bus.CodeUnauthorized, Message: "agent_id not allowlisted", Status: 401})
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		writeBusError(w, &bus.Error{Code: bus.CodeValidation, Message: "secret is required", Status: 400})
		return
	}

	agent, err := s.store.RegisterAgent(bus.RegisterAgentInput{
		AgentID:      req.AgentID,
		Capabilities: req.Capabilities,
		Description:  req.Description,
		Mode:         bus.AgentMode(req.Mode),
		CallbackURL:  req.CallbackURL,
		TTLSeconds:   req.TTL,
	})
	if err != nil {
		writeBusError(w, err)
		return
	}
	s.setAgentSecret(agent.AgentID, req.Secret)

	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"agent_id":   agent.AgentID,
		"expires_at": agent.ExpiresAt,
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	agents := s.store.ListAgents(strings.TrimSpace(r.URL.Query().Get("capability")))
	writeJSON(w, 200, map[string]any{"agents": agents})
}

func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		blob, err := readBody(r)
		if err != nil {
			writeBusError(w, bus.NewValidationJSONError(err))
			return
		}
		var req struct {
			ConversationID string   `json:"conversation_id"`
			Title          string   `json:"title"`
			Participants   []string `json:"participants"`
			Meta           any      `json:"meta"`
		}
		if err := decodeJSONBytes(blob, &req); err != nil {
			writeBusError(w, bus.NewValidationJSONError(err))
			return
		}
		c, err := s.store.CreateConversation(bus.CreateConversationInput{
			ConversationID: req.ConversationID,
			Title:          req.Title,
			Participants:   req.Participants,
			Meta:           req.Meta,
		})
		if err != nil {
			writeBusError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "conversation_id": c.ConversationID})
	case http.MethodGet:
		filter := bus.ListConversationsFilter{
			Participant: strings.TrimSpace(r.URL.Query().Get("participant")),
			Status:      strings.TrimSpace(r.URL.Query().Get("status")),
		}
		conversations := s.store.ListConversations(filter)
		writeJSON(w, 200, map[string]any{"conversations": conversations})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/conversations/")
	if !strings.HasSuffix(path, "/messages") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	conversationID := strings.TrimSuffix(path, "/messages")
	conversationID = strings.TrimSuffix(conversationID, "/")
	if conversationID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	cursor := parseInt(r.URL.Query().Get("cursor"), 0)
	limit := parseInt(r.URL.Query().Get("limit"), 50)
	cid, messages, next, err := s.store.ListConversationMessages(bus.ListConversationMessagesInput{
		ConversationID: conversationID,
		Cursor:         cursor,
		Limit:          limit,
	})
	if err != nil {
		writeBusError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"conversation_id": cid,
		"messages":        messages,
		"cursor":          strconv.Itoa(next),
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	blob, err := readBody(r)
	if err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	var req struct {
		To             string           `json:"to"`
		From           string           `json:"from"`
		ConversationID string           `json:"conversation_id"`
		RequestID      string           `json:"request_id"`
		Type           string           `json:"type"`
		Body           string           `json:"body"`
		Meta           any              `json:"meta"`
		Attachments    []bus.Attachment `json:"attachments"`
		TTL            int              `json:"ttl"`
		InReplyTo      string           `json:"in_reply_to"`
	}
	if err := decodeJSONBytes(blob, &req); err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	if err := s.verifySignature(req.From, r.Header.Get("X-Bus-Signature"), blob); err != nil {
		writeBusError(w, err)
		return
	}

	message, duplicate, err := s.store.SendMessage(bus.SendMessageInput{
		To:             req.To,
		From:           req.From,
		ConversationID: req.ConversationID,
		RequestID:      req.RequestID,
		Type:           bus.MessageType(req.Type),
		Body:           req.Body,
		Meta:           req.Meta,
		Attachments:    req.Attachments,
		TTLSeconds:     req.TTL,
		InReplyTo:      req.InReplyTo,
	})
	if err != nil {
		writeBusError(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"message_id": message.MessageID,
		"duplicate":  duplicate,
	})
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	sigPayload := []byte(r.URL.RawQuery)
	if err := s.verifySignature(agentID, r.Header.Get("X-Bus-Signature"), sigPayload); err != nil {
		writeBusError(w, err)
		return
	}

	cursor := parseInt(r.URL.Query().Get("cursor"), 0)
	wait := parseWaitSeconds(r.URL.Query().Get("wait"))

	events, next, err := s.store.PollInbox(bus.PollInboxInput{
		AgentID: agentID,
		Cursor:  cursor,
		Wait:    wait,
	})
	if err != nil {
		writeBusError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"events": events,
		"cursor": strconv.Itoa(next),
	})
}

func (s *Server) handleAcks(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	blob, err := readBody(r)
	if err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	var req struct {
		AgentID   string `json:"agent_id"`
		MessageID string `json:"message_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
	}
	if err := decodeJSONBytes(blob, &req); err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	if err := s.verifySignature(req.AgentID, r.Header.Get("X-Bus-Signature"), blob); err != nil {
		writeBusError(w, err)
		return
	}
	if err := s.store.Ack(bus.AckInput{
		AgentID:   req.AgentID,
		MessageID: req.MessageID,
		Status:    req.Status,
		Reason:    req.Reason,
	}); err != nil {
		writeBusError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	blob, err := readBody(r)
	if err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	var req struct {
		MessageID string `json:"message_id"`
		Type      string `json:"type"`
		Body      string `json:"body"`
		Meta      any    `json:"meta"`
	}
	if err := decodeJSONBytes(blob, &req); err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	actor := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if err := s.verifySignature(actor, r.Header.Get("X-Bus-Signature"), blob); err != nil {
		writeBusError(w, err)
		return
	}
	if err := s.store.PostEvent(bus.EventInput{
		ActorAgentID: actor,
		MessageID:    req.MessageID,
		Type:         req.Type,
		Body:         req.Body,
		Meta:         req.Meta,
	}); err != nil {
		writeBusError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func parseObserveCursor(r *http.Request) int64 {
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor == "" {
		cursor = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if cursor == "" {
		return 0
	}
	v, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func (s *Server) handleObserve(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeBusError(w, bus.NewInternalError("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	cursor := parseObserveCursor(r)
	filter := bus.ObserveFilter{
		ConversationID: strings.TrimSpace(r.URL.Query().Get("conversation_id")),
		AgentID:        strings.TrimSpace(r.URL.Query().Get("agent_id")),
	}

	bw := bufio.NewWriter(w)
	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		events, last := s.store.ObserveSince(cursor, filter, 1*time.Second)
		if len(events) == 0 {
			if _, err := bw.WriteString(": keep-alive\n\n"); err != nil {
				return
			}
			if err := bw.Flush(); err != nil {
				return
			}
			flusher.Flush()
			continue
		}

		for _, evt := range events {
			blob, err := json.Marshal(evt.Data)
			if err != nil {
				continue
			}
			if _, err := bw.WriteString(fmt.Sprintf("id: %d\n", evt.ID)); err != nil {
				return
			}
			if _, err := bw.WriteString(fmt.Sprintf("event: %s\n", evt.Type)); err != nil {
				return
			}
			if _, err := bw.WriteString("data: "); err != nil {
				return
			}
			if _, err := bw.Write(blob); err != nil {
				return
			}
			if _, err := bw.WriteString("\n\n"); err != nil {
				return
			}
		}
		if err := bw.Flush(); err != nil {
			return
		}
		flusher.Flush()
		cursor = last
	}
}

func (s *Server) handleInject(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	blob, err := readBody(r)
	if err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	var req struct {
		Identity       string `json:"identity"`
		ConversationID string `json:"conversation_id"`
		To             string `json:"to"`
		Body           string `json:"body"`
	}
	if err := decodeJSONBytes(blob, &req); err != nil {
		writeBusError(w, bus.NewValidationJSONError(err))
		return
	}
	message, err := s.store.Inject(bus.InjectInput{
		Identity:       req.Identity,
		ConversationID: req.ConversationID,
		To:             req.To,
		Body:           req.Body,
	})
	if err != nil {
		writeBusError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message_id": message.MessageID})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, 200, s.store.Health())
}

func (s *Server) handleTopLevelHealth(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, 200, s.store.Health())
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, s.store.Metrics())
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, 200, s.store.SystemStatus())
}
