package busclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		_, _ = w.Write([]byte(`{"ok":true,"agent_id":"travel-agent"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.RegisterAgentWithDescription(context.Background(), "travel-agent", "secret", []string{"query:travel-status"}, "Travel inbox agent for flights, trips, and hotels.")
	if err != nil {
		t.Fatalf("RegisterAgentWithDescription() error = %v", err)
	}

	if got.AgentID != "travel-agent" {
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
		_, _ = w.Write([]byte(`{"ok":true,"agent_id":"travel-agent"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.RegisterAgentWithPassport(context.Background(), RegisterAgentRequest{
		AgentID:       "travel-agent",
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
		_, _ = w.Write([]byte(`{"agents":[{"agent_id":"polsia-agent","capabilities":["query:polsia-status"],"version":"v0.5.0","description":"Polsia status reports and action items.","agent_class":"worker","mutation_class":"observe","build":{"commit":"abc1234","dirty":false},"meta":{"owner":"pinakes","repo":"github.com/joelkehle/pinakes","health_url":"http://polsia-agent/health","dependencies":["gmail-api"]},"status":"active"}]}`))
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
