package busclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientWithTimeoutUsesCallerDeadline(t *testing.T) {
	t.Parallel()

	client := NewClientWithTimeout("http://example.test", 90*time.Second)
	if client.http.Timeout != 90*time.Second {
		t.Fatalf("HTTP timeout = %s, want 90s", client.http.Timeout)
	}
	defaulted := NewClientWithTimeout("http://example.test", 0)
	if defaulted.http.Timeout != defaultHTTPTimeout {
		t.Fatalf("default HTTP timeout = %s, want %s", defaulted.http.Timeout, defaultHTTPTimeout)
	}
}

func TestDoJSONReportsPartialResponseRead(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "20")
		_, _ = w.Write([]byte(`{"ok":`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, _, err := client.DoJSON(context.Background(), http.MethodGet, "/partial", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "read GET /partial response") {
		t.Fatalf("DoJSON error = %v, want explicit partial-read error", err)
	}
}

func TestPollInboxLimitedSendsSignedLimit(t *testing.T) {
	t.Parallel()

	const secret = "secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "8" {
			t.Fatalf("limit = %q, want 8", r.URL.Query().Get("limit"))
		}
		if got, want := r.Header.Get("X-Bus-Signature"), Sign(secret, []byte(r.URL.RawQuery)); got != want {
			t.Fatalf("signature = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"events":[],"cursor":"7"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, cursor, err := client.PollInboxLimited(context.Background(), "ucla.reader", secret, 3, 0, 8)
	if err != nil || cursor != 7 {
		t.Fatalf("PollInboxLimited cursor=%d error=%v", cursor, err)
	}
}

func TestRegisterAgentWithDescriptionSendsDescription(t *testing.T) {
	t.Parallel()

	var got struct {
		AgentID      string   `json:"agent_id"`
		Capabilities []string `json:"capabilities"`
		Description  string   `json:"description"`
		Mode         string   `json:"mode"`
		TTL          int      `json:"ttl"`
		Secret       string   `json:"secret"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/register" {
			t.Fatalf("path = %s, want /v1/agents/register", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode register body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"agent_id":"ucla.travel-agent"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.RegisterAgentWithDescription(context.Background(), "ucla.travel-agent", "secret", []string{"query:travel-status"}, "Travel inbox agent for flights, trips, and hotels.")
	if err != nil {
		t.Fatalf("RegisterAgentWithDescription() error = %v", err)
	}

	if got.AgentID != "ucla.travel-agent" {
		t.Fatalf("agent_id = %q, want travel-agent", got.AgentID)
	}
	if got.Description != "Travel inbox agent for flights, trips, and hotels." {
		t.Fatalf("description = %q", got.Description)
	}
	if got.Mode != "pull" {
		t.Fatalf("mode = %q, want pull", got.Mode)
	}
	if got.TTL != 120 {
		t.Fatalf("ttl = %d, want 120", got.TTL)
	}
}

func TestRegisterAgentWithPassportSendsPassportFields(t *testing.T) {
	t.Parallel()

	var got RegisterAgentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/register" {
			t.Fatalf("path = %s, want /v1/agents/register", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode register body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"agent_id":"ucla.travel-agent"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.RegisterAgentWithPassport(context.Background(), RegisterAgentRequest{
		AgentID:       "ucla.travel-agent",
		Secret:        "secret",
		Capabilities:  []string{"query:travel-status"},
		Version:       "v0.5.0",
		Description:   "Travel inbox agent for flights, trips, and hotels.",
		AgentClass:    "worker",
		MutationClass: "observe",
		Build: &BuildInfo{
			Commit: "abc1234",
			Dirty:  false,
		},
		Meta: &AgentMeta{
			Owner:        "pinakes",
			Repo:         "github.com/joelkehle/pinakes",
			HealthURL:    "http://travel-agent/health",
			Dependencies: []string{"gmail-api"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterAgentWithPassport() error = %v", err)
	}

	if got.AgentClass != "worker" {
		t.Fatalf("agent_class = %q, want worker", got.AgentClass)
	}
	if got.MutationClass != "observe" {
		t.Fatalf("mutation_class = %q, want observe", got.MutationClass)
	}
	if got.Build == nil || got.Build.Commit != "abc1234" || got.Build.Dirty {
		t.Fatalf("build = %#v", got.Build)
	}
	if got.Meta == nil || got.Meta.HealthURL != "http://travel-agent/health" {
		t.Fatalf("meta = %#v", got.Meta)
	}
	if got.Mode != "pull" {
		t.Fatalf("mode = %q, want pull", got.Mode)
	}
	if got.TTL != 120 {
		t.Fatalf("ttl = %d, want 120", got.TTL)
	}
}

func TestListAgentsDecodesDescription(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents" {
			t.Fatalf("path = %s, want /v1/agents", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[{"agent_id":"ucla.polsia-agent","capabilities":["query:polsia-status"],"version":"v0.5.0","description":"Polsia status reports and action items.","agent_class":"worker","mutation_class":"observe","build":{"commit":"abc1234","dirty":false},"meta":{"owner":"pinakes","repo":"github.com/joelkehle/pinakes","health_url":"http://polsia-agent/health","dependencies":["gmail-api"]},"status":"active"}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	agents, err := client.ListAgents(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(agents))
	}
	if agents[0].Description != "Polsia status reports and action items." {
		t.Fatalf("description = %q", agents[0].Description)
	}
	if agents[0].Version != "v0.5.0" {
		t.Fatalf("version = %q", agents[0].Version)
	}
	if agents[0].Build == nil || agents[0].Build.Commit != "abc1234" || agents[0].Build.Dirty {
		t.Fatalf("build = %#v", agents[0].Build)
	}
	if agents[0].Meta == nil || agents[0].Meta.HealthURL != "http://polsia-agent/health" {
		t.Fatalf("meta = %#v", agents[0].Meta)
	}
}

func TestCreateConversationSignsBody(t *testing.T) {
	t.Parallel()

	const secret = "secret-a"
	var gotAgentID, gotSignature string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/conversations" {
			t.Fatalf("path = %s, want /v1/conversations", r.URL.Path)
		}
		gotAgentID = r.Header.Get("X-Agent-ID")
		gotSignature = r.Header.Get("X-Bus-Signature")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"conversation_id":"conv-1"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	conversationID, err := client.CreateConversation(context.Background(), "agent-a", secret, CreateConversationRequest{
		ConversationID: "conv-1",
		Title:          "Test",
		Participants:   []string{"agent-a", "agent-b"},
	})
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	if conversationID != "conv-1" {
		t.Fatalf("conversationID = %q, want conv-1", conversationID)
	}
	if gotAgentID != "agent-a" {
		t.Fatalf("X-Agent-ID = %q, want agent-a", gotAgentID)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(gotBody)
	wantSignature := hex.EncodeToString(mac.Sum(nil))
	if gotSignature != wantSignature {
		t.Fatalf("signature = %q, want %q", gotSignature, wantSignature)
	}
}
