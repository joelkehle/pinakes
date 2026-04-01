package busclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Attachment struct {
	URL         string `json:"url"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type InboxEvent struct {
	MessageID      string       `json:"message_id"`
	Type           string       `json:"type"`
	From           string       `json:"from"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Body           string       `json:"body"`
	Meta           any          `json:"meta,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
}

type BuildInfo struct {
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty"`
}

type AgentMeta struct {
	Owner        string   `json:"owner,omitempty"`
	Repo         string   `json:"repo,omitempty"`
	HealthURL    string   `json:"health_url,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type AgentInfo struct {
	AgentID       string     `json:"agent_id"`
	Capabilities  []string   `json:"capabilities"`
	Version       string     `json:"version,omitempty"`
	Description   string     `json:"description,omitempty"`
	AgentClass    string     `json:"agent_class,omitempty"`
	MutationClass string     `json:"mutation_class,omitempty"`
	Build         *BuildInfo `json:"build,omitempty"`
	Meta          *AgentMeta `json:"meta,omitempty"`
	Status        string     `json:"status"`
}

type RegisterAgentRequest struct {
	AgentID       string     `json:"agent_id"`
	Secret        string     `json:"secret"`
	Capabilities  []string   `json:"capabilities"`
	Version       string     `json:"version,omitempty"`
	Description   string     `json:"description,omitempty"`
	AgentClass    string     `json:"agent_class,omitempty"`
	MutationClass string     `json:"mutation_class,omitempty"`
	Build         *BuildInfo `json:"build,omitempty"`
	Meta          *AgentMeta `json:"meta,omitempty"`
	Mode          string     `json:"mode"`
	CallbackURL   string     `json:"callback_url,omitempty"`
	TTL           int        `json:"ttl"`
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func Sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) DoJSON(ctx context.Context, method, path string, payload []byte, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return blob, resp.StatusCode, fmt.Errorf("%s %s failed status=%d body=%s", method, path, resp.StatusCode, string(blob))
	}
	return blob, resp.StatusCode, nil
}

func (c *Client) RegisterAgent(ctx context.Context, agentID, secret string, capabilities []string) error {
	return c.RegisterAgentWithDescription(ctx, agentID, secret, capabilities, "")
}

func (c *Client) RegisterAgentWithDescription(ctx context.Context, agentID, secret string, capabilities []string, description string) error {
	return c.RegisterAgentWithPassport(ctx, RegisterAgentRequest{
		AgentID:      agentID,
		Secret:       secret,
		Capabilities: capabilities,
		Description:  strings.TrimSpace(description),
		Mode:         "pull",
		TTL:          120,
	})
}

func (c *Client) RegisterAgentWithPassport(ctx context.Context, req RegisterAgentRequest) error {
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = "pull"
	}
	if req.TTL <= 0 {
		req.TTL = 120
	}
	body, _ := json.Marshal(req)
	_, _, err := c.DoJSON(ctx, http.MethodPost, "/v1/agents/register", body, nil)
	return err
}

func (c *Client) SendMessage(ctx context.Context, from, secret, to, conversationID, requestID, messageType, bodyText string, attachments []Attachment, meta map[string]any) (string, error) {
	payload := map[string]any{
		"to":              to,
		"from":            from,
		"conversation_id": conversationID,
		"request_id":      requestID,
		"type":            messageType,
		"body":            bodyText,
		"attachments":     attachments,
		"meta":            meta,
	}
	blob, _ := json.Marshal(payload)
	headers := map[string]string{"X-Bus-Signature": Sign(secret, blob)}
	out, _, err := c.DoJSON(ctx, http.MethodPost, "/v1/messages", blob, headers)
	if err != nil {
		return "", err
	}
	var resp struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.MessageID) == "" {
		return "", fmt.Errorf("missing message_id in response")
	}
	return resp.MessageID, nil
}

func (c *Client) PollInbox(ctx context.Context, agentID, secret string, cursor int, waitSec int) ([]InboxEvent, int, error) {
	q := url.Values{}
	q.Set("agent_id", agentID)
	q.Set("cursor", strconv.Itoa(cursor))
	q.Set("wait", strconv.Itoa(waitSec))
	rawQuery := q.Encode()
	path := "/v1/inbox?" + rawQuery
	headers := map[string]string{"X-Bus-Signature": Sign(secret, []byte(rawQuery))}
	out, _, err := c.DoJSON(ctx, http.MethodGet, path, nil, headers)
	if err != nil {
		return nil, cursor, err
	}
	var resp struct {
		Events []InboxEvent `json:"events"`
		Cursor string       `json:"cursor"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, cursor, err
	}
	next, err := strconv.Atoi(strings.TrimSpace(resp.Cursor))
	if err != nil {
		next = cursor
	}
	return resp.Events, next, nil
}

func (c *Client) Ack(ctx context.Context, agentID, secret, messageID, status, reason string) error {
	payload := map[string]any{
		"agent_id":   agentID,
		"message_id": messageID,
		"status":     status,
		"reason":     reason,
	}
	blob, _ := json.Marshal(payload)
	headers := map[string]string{"X-Bus-Signature": Sign(secret, blob)}
	_, _, err := c.DoJSON(ctx, http.MethodPost, "/v1/acks", blob, headers)
	return err
}

func (c *Client) Event(ctx context.Context, agentID, secret, messageID, eventType, body string, meta map[string]any) error {
	payload := map[string]any{
		"message_id": messageID,
		"type":       eventType,
		"body":       body,
		"meta":       meta,
	}
	blob, _ := json.Marshal(payload)
	headers := map[string]string{
		"X-Agent-ID":      agentID,
		"X-Bus-Signature": Sign(secret, blob),
	}
	_, _, err := c.DoJSON(ctx, http.MethodPost, "/v1/events", blob, headers)
	return err
}

func (c *Client) ListAgents(ctx context.Context, capability string) ([]AgentInfo, error) {
	path := "/v1/agents"
	if capability != "" {
		path += "?capability=" + url.QueryEscape(capability)
	}
	out, _, err := c.DoJSON(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Agents []AgentInfo `json:"agents"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	return resp.Agents, nil
}
