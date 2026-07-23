package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

const (
	testInjectToken  = "test-inject-token"
	testObserveToken = "test-observe-token"
)

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func contractConfig(now time.Time) bus.Config {
	return bus.Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock: func() time.Time {
			return now
		},
	}
}

func newContractServer() http.Handler {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	store := bus.NewStore(contractConfig(now))
	return NewServer(store)
}

func newContractServerPersistent(t *testing.T) http.Handler {
	t.Helper()
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	statePath := t.TempDir() + "/state.json"
	store, err := bus.NewPersistentStore(statePath, contractConfig(now))
	if err != nil {
		t.Fatalf("new persistent store: %v", err)
	}
	return NewServer(store)
}

func newContractServerWithEnv(t *testing.T, env map[string]string) http.Handler {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
	return newContractServer()
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(blob)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	return resp
}

func mustStatus(t *testing.T, resp *http.Response, want int) []byte {
	t.Helper()
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, want, string(blob))
	}
	return blob
}

func runContractAllEndpoints(t *testing.T, h http.Handler) {
	t.Helper()
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	regA := map[string]any{"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a"}
	regB := map[string]any{"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regA, nil), 200)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regB, nil), 200)

	blobAgents := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), 200)
	if !bytes.Contains(blobAgents, []byte("\"agent_id\":\"ucla.a\"")) || !bytes.Contains(blobAgents, []byte("\"agent_id\":\"ucla.b\"")) {
		t.Fatalf("expected agents list to include a and b: %s", string(blobAgents))
	}

	convReq := map[string]any{"conversation_id": "conv-1", "title": "test", "participants": []string{"ucla.a", "ucla.b"}, "meta": map[string]any{"case": "c1"}}
	convBlob, _ := json.Marshal(convReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", convReq, map[string]string{
		"X-Agent-ID":      "ucla.a",
		"X-Bus-Signature": signPayload("secret-a", convBlob),
	}), 200)
	blobConvs := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), 200)
	if !bytes.Contains(blobConvs, []byte("conv-1")) {
		t.Fatalf("expected conversation listing to include conv-1: %s", string(blobConvs))
	}

	sendReq := map[string]any{
		"to": "ucla.b", "from": "ucla.a", "conversation_id": "conv-1", "request_id": "rid-1", "type": "request", "body": "do work",
	}
	sendBlob, _ := json.Marshal(sendReq)
	blobSend := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq, map[string]string{"X-Bus-Signature": signPayload("secret-a", sendBlob)}), 200)
	var sendResp struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(blobSend, &sendResp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if sendResp.MessageID == "" {
		t.Fatalf("expected message_id in send response")
	}

	query := "agent_id=ucla.b&cursor=0&wait=0"
	respInbox := doJSON(t, c, http.MethodGet, ts.URL+"/v1/inbox?"+query, nil, map[string]string{"X-Bus-Signature": signPayload("secret-b", []byte(query))})
	blobInbox := mustStatus(t, respInbox, 200)
	if !bytes.Contains(blobInbox, []byte(sendResp.MessageID)) {
		t.Fatalf("expected inbox to include message: %s", string(blobInbox))
	}

	ackReq := map[string]any{"agent_id": "ucla.b", "message_id": sendResp.MessageID, "status": "accepted"}
	ackBlob, _ := json.Marshal(ackReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/acks", ackReq, map[string]string{"X-Bus-Signature": signPayload("secret-b", ackBlob)}), 200)

	evtReq := map[string]any{"message_id": sendResp.MessageID, "type": "progress", "body": "50%"}
	evtBlob, _ := json.Marshal(evtReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/events", evtReq, map[string]string{"X-Agent-ID": "ucla.b", "X-Bus-Signature": signPayload("secret-b", evtBlob)}), 200)

	evtFinalReq := map[string]any{"message_id": sendResp.MessageID, "type": "final", "body": "done"}
	evtFinalBlob, _ := json.Marshal(evtFinalReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/events", evtFinalReq, map[string]string{"X-Agent-ID": "ucla.b", "X-Bus-Signature": signPayload("secret-b", evtFinalBlob)}), 200)

	blobHistory := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-1/messages", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), 200)
	if !bytes.Contains(blobHistory, []byte(sendResp.MessageID)) {
		t.Fatalf("expected history to include message: %s", string(blobHistory))
	}

	injectReq := map[string]any{"identity": "joel", "conversation_id": "conv-1", "to": "ucla.b", "body": "human note"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", injectReq, map[string]string{
		"Authorization": "Bearer " + testInjectToken,
	}), 200)

	healthBody := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/health", nil, nil), 200)
	var health struct {
		OK      bool   `json:"ok"`
		Status  string `json:"status"`
		Agents  int    `json:"agents"`
		Observe int    `json:"observe"`
		Push    struct {
			Successes int `json:"successes"`
			Failures  int `json:"failures"`
		} `json:"push"`
	}
	if err := json.Unmarshal(healthBody, &health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !health.OK || health.Status != "healthy" {
		t.Fatalf("unexpected health payload: %s", string(healthBody))
	}
	if health.Agents != 2 {
		t.Fatalf("health agents=%d want=2 payload=%s", health.Agents, string(healthBody))
	}

	systemBody := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/system/status", nil, nil), 200)
	var system struct {
		OK     bool `json:"ok"`
		System struct {
			AgentsActive  int `json:"agents_active"`
			AgentsExpired int `json:"agents_expired"`
			Conversations int `json:"conversations"`
			Messages      int `json:"messages"`
			ObserveEvents int `json:"observe_events"`
			PushSuccesses int `json:"push_successes"`
			PushFailures  int `json:"push_failures"`
		} `json:"system"`
	}
	if err := json.Unmarshal(systemBody, &system); err != nil {
		t.Fatalf("decode system status response: %v", err)
	}
	if !system.OK {
		t.Fatalf("unexpected system status payload: %s", string(systemBody))
	}
	if system.System.AgentsActive != 2 || system.System.AgentsExpired != 0 {
		t.Fatalf("unexpected agent counts in system status: %s", string(systemBody))
	}
	if system.System.Conversations != 1 {
		t.Fatalf("unexpected conversation count in system status: %s", string(systemBody))
	}
	if system.System.Messages != 2 {
		t.Fatalf("unexpected message count in system status: %s", string(systemBody))
	}
}

func TestContractAllEndpoints(t *testing.T) {
	runContractAllEndpoints(t, newContractServerWithEnv(t, map[string]string{
		"INJECT_TOKENS":  testInjectToken,
		"OBSERVE_TOKENS": testObserveToken,
	}))
}

func TestContractAllEndpointsPersistentBackend(t *testing.T) {
	t.Setenv("INJECT_TOKENS", testInjectToken)
	t.Setenv("OBSERVE_TOKENS", testObserveToken)
	runContractAllEndpoints(t, newContractServerPersistent(t))
}

func TestContractJSONSecretsSurviveRestartWithoutReregistration(t *testing.T) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	statePath := t.TempDir() + "/state.json"
	store1, err := bus.NewPersistentStore(statePath, contractConfig(now))
	if err != nil {
		t.Fatalf("new persistent store: %v", err)
	}
	h1 := NewServer(store1)
	ts1 := httptest.NewServer(h1)
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts1.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts1.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)
	ts1.CloseClientConnections()
	ts1.Close()

	store2, err := bus.NewPersistentStore(statePath, contractConfig(now))
	if err != nil {
		t.Fatalf("reopen persistent store: %v", err)
	}
	h2 := NewServer(store2)
	ts2 := httptest.NewServer(h2)
	defer func() {
		ts2.CloseClientConnections()
		ts2.Close()
	}()

	sendReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-json-secret-restart", "type": "request", "body": "after restart"}
	sendBlob, _ := json.Marshal(sendReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts2.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", sendBlob),
	}), http.StatusOK)
}

func TestContractSQLiteSecretsSurviveRestartWithoutReregistration(t *testing.T) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	dbPath := t.TempDir() + "/state.db"
	store1, err := bus.NewSQLiteStore(dbPath, contractConfig(now))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	h1 := NewServer(store1)
	ts1 := httptest.NewServer(h1)
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts1.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts1.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)
	ts1.CloseClientConnections()
	ts1.Close()
	if err := store1.Close(); err != nil {
		t.Fatalf("close first sqlite store: %v", err)
	}

	store2, err := bus.NewSQLiteStore(dbPath, contractConfig(now))
	if err != nil {
		t.Fatalf("reopen sqlite store: %v", err)
	}
	defer store2.Close()
	h2 := NewServer(store2)
	ts2 := httptest.NewServer(h2)
	defer func() {
		ts2.CloseClientConnections()
		ts2.Close()
	}()

	sendReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-sqlite-secret-restart", "type": "request", "body": "after restart"}
	sendBlob, _ := json.Marshal(sendReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts2.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", sendBlob),
	}), http.StatusOK)
}

func TestContractListAgentsDoesNotLeakSecrets(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.agent-no-leak", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "supersecret-no-leak",
	}, nil), http.StatusOK)
	body := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), http.StatusOK)
	if bytes.Contains(body, []byte("supersecret-no-leak")) || bytes.Contains(body, []byte(`"secret"`)) {
		t.Fatalf("agent listing leaked secret material: %s", string(body))
	}
}

func TestContractReregistrationRejectsSecretHijack(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)

	conflict := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "attacker-secret",
	}, nil), http.StatusConflict)
	if !bytes.Contains(conflict, []byte("re-registration requires proof of current secret")) {
		t.Fatalf("unexpected conflict response: %s", string(conflict))
	}

	sendReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-hijack-old", "type": "request", "body": "old secret still works"}
	sendBlob, _ := json.Marshal(sendReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", sendBlob),
	}), http.StatusOK)

	badReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-hijack-new", "type": "request", "body": "new secret must fail"}
	badBlob, _ := json.Marshal(badReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", badReq, map[string]string{
		"X-Bus-Signature": signPayload("attacker-secret", badBlob),
	}), http.StatusUnauthorized)
}

func TestContractReregistrationRotationRequiresOldSecretSignature(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)

	rotateReq := map[string]any{"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a-rotated"}
	rotateBlob, _ := json.Marshal(rotateReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", rotateReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", rotateBlob),
	}), http.StatusOK)

	oldReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-rotation-old", "type": "request", "body": "old secret rejected"}
	oldBlob, _ := json.Marshal(oldReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", oldReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", oldBlob),
	}), http.StatusUnauthorized)

	newReq := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-rotation-new", "type": "request", "body": "new secret works"}
	newBlob, _ := json.Marshal(newReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", newReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a-rotated", newBlob),
	}), http.StatusOK)
}

func TestContractReregistrationSameSecretStillIdempotent(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"x"}, "mode": "pull", "ttl": 60, "secret": "secret-a", "version": "v1",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"x"}, "mode": "pull", "ttl": 60, "secret": "secret-a", "version": "v2",
	}, nil), http.StatusOK)

	body := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), http.StatusOK)
	if !bytes.Contains(body, []byte(`"version":"v2"`)) {
		t.Fatalf("expected idempotent re-registration to update fields: %s", string(body))
	}
}

func TestContractLegacyEmptySecretReregistrationGrace(t *testing.T) {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	store := bus.NewStore(contractConfig(now))
	if _, err := store.RegisterAgent(bus.RegisterAgentInput{AgentID: "ucla.legacy", Mode: bus.AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("seed legacy agent: %v", err)
	}
	ts := httptest.NewServer(NewServer(store))
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.legacy", "capabilities": []string{"x"}, "mode": "pull", "ttl": 60, "secret": "secret-legacy",
	}, nil), http.StatusOK)
}

func TestContractPassportRegistrationRoundTrip(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.legacy", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-legacy",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id":       "ucla.passport",
		"mode":           "pull",
		"capabilities":   []string{"x", "y"},
		"secret":         "secret-passport",
		"version":        "v0.5.0",
		"description":    "Passport-capable worker.",
		"agent_class":    "worker",
		"mutation_class": "observe",
		"build": map[string]any{
			"commit": "abc1234",
			"dirty":  false,
		},
		"meta": map[string]any{
			"owner":        "pinakes",
			"repo":         "github.com/joelkehle/pinakes",
			"health_url":   "http://passport/health",
			"dependencies": []string{"sqlite", "openai-api"},
		},
	}, nil), http.StatusOK)

	body := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), http.StatusOK)
	var resp struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d body=%s", len(resp.Agents), string(body))
	}

	var legacy, passport map[string]any
	for _, agent := range resp.Agents {
		switch agent["agent_id"] {
		case "ucla.legacy":
			legacy = agent
		case "ucla.passport":
			passport = agent
		}
	}
	if legacy == nil || passport == nil {
		t.Fatalf("expected legacy and passport agents in response: %s", string(body))
	}
	if _, ok := legacy["version"]; ok {
		t.Fatalf("legacy agent should omit version: %v", legacy)
	}
	if _, ok := legacy["build"]; ok {
		t.Fatalf("legacy agent should omit build: %v", legacy)
	}
	if _, ok := legacy["meta"]; ok {
		t.Fatalf("legacy agent should omit meta: %v", legacy)
	}
	if got := passport["version"]; got != "v0.5.0" {
		t.Fatalf("version=%v want v0.5.0", got)
	}
	if got := passport["agent_class"]; got != "worker" {
		t.Fatalf("agent_class=%v want worker", got)
	}
	if got := passport["mutation_class"]; got != "observe" {
		t.Fatalf("mutation_class=%v want observe", got)
	}
	build, ok := passport["build"].(map[string]any)
	if !ok {
		t.Fatalf("expected build object, got %T (%v)", passport["build"], passport["build"])
	}
	if got := build["commit"]; got != "abc1234" {
		t.Fatalf("build.commit=%v want abc1234", got)
	}
	if got := build["dirty"]; got != false {
		t.Fatalf("build.dirty=%v want false", got)
	}
	meta, ok := passport["meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected meta object, got %T (%v)", passport["meta"], passport["meta"])
	}
	if got := meta["health_url"]; got != "http://passport/health" {
		t.Fatalf("meta.health_url=%v want http://passport/health", got)
	}
}

func TestContractPassportValidation(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	invalidClass := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.bad-class", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret", "agent_class": "router",
	}, nil), http.StatusBadRequest)
	if !bytes.Contains(invalidClass, []byte("agent_class must be worker or orchestrator")) {
		t.Fatalf("unexpected invalid class response: %s", string(invalidClass))
	}

	invalidMutation := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.bad-mutation", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret", "mutation_class": "write",
	}, nil), http.StatusBadRequest)
	if !bytes.Contains(invalidMutation, []byte("mutation_class must be observe, recommend, or mutate")) {
		t.Fatalf("unexpected invalid mutation response: %s", string(invalidMutation))
	}
}

func TestContractPassportReregistrationUpdatesFields(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	register := func(version, description, mutationClass, commit string, dirty bool, signReregistration bool) {
		body := map[string]any{
			"agent_id":       "ucla.passport",
			"mode":           "pull",
			"capabilities":   []string{"x"},
			"secret":         "secret-passport",
			"version":        version,
			"description":    description,
			"agent_class":    "worker",
			"mutation_class": mutationClass,
			"build": map[string]any{
				"commit": commit,
				"dirty":  dirty,
			},
		}
		headers := map[string]string(nil)
		if signReregistration {
			blob, _ := json.Marshal(body)
			headers = map[string]string{"X-Bus-Signature": signPayload("secret-passport", blob)}
		}
		mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", body, headers), http.StatusOK)
	}

	register("v0.5.0", "first", "observe", "abc1234", false, false)
	register("v0.5.1", "second", "recommend", "def5678", true, true)

	body := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), http.StatusOK)
	var resp struct {
		Agents []struct {
			AgentID       string `json:"agent_id"`
			Version       string `json:"version"`
			Description   string `json:"description"`
			AgentClass    string `json:"agent_class"`
			MutationClass string `json:"mutation_class"`
			Build         struct {
				Commit string `json:"commit"`
				Dirty  bool   `json:"dirty"`
			} `json:"build"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	if len(resp.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d body=%s", len(resp.Agents), string(body))
	}
	got := resp.Agents[0]
	if got.Version != "v0.5.1" || got.Description != "second" || got.AgentClass != "worker" || got.MutationClass != "recommend" || got.Build.Commit != "def5678" || !got.Build.Dirty {
		t.Fatalf("unexpected re-registration payload: %+v", got)
	}
}

type sseEvent struct {
	ID   string
	Type string
	Data string
}

func readNextSSEEvent(t *testing.T, r io.Reader, timeout time.Duration) sseEvent {
	t.Helper()
	events := make(chan sseEvent, 1)
	errs := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		out := sseEvent{}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if out.ID != "" || out.Type != "" || out.Data != "" {
					events <- out
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			if strings.HasPrefix(line, "id: ") {
				out.ID = strings.TrimPrefix(line, "id: ")
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				out.Type = strings.TrimPrefix(line, "event: ")
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				out.Data = strings.TrimPrefix(line, "data: ")
				continue
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		errs <- io.EOF
	}()

	select {
	case evt := <-events:
		return evt
	case err := <-errs:
		t.Fatalf("sse stream ended before event: %v", err)
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for sse event")
	}
	return sseEvent{}
}

func TestObserveSSECursorResume(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	t.Cleanup(func() { ts.CloseClientConnections() })
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{"agent_id": "ucla.a", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-a"}, nil), 200)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{"agent_id": "ucla.b", "mode": "pull", "capabilities": []string{"y"}, "secret": "secret-b"}, nil), 200)

	ctxObserve, cancelObserve := context.WithCancel(context.Background())
	defer cancelObserve()
	reqObserve, _ := http.NewRequestWithContext(ctxObserve, http.MethodGet, ts.URL+"/v1/observe", nil)
	reqObserve.Close = true
	reqObserve.Header.Set("X-Agent-ID", "ucla.a")
	reqObserve.Header.Set("X-Bus-Signature", signPayload("secret-a", []byte(reqObserve.URL.RawQuery)))
	respObserve, err := c.Do(reqObserve)
	if err != nil {
		t.Fatalf("open observe: %v", err)
	}
	defer respObserve.Body.Close()

	sendReq1 := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-sse-1", "type": "request", "body": "one"}
	blob1, _ := json.Marshal(sendReq1)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq1, map[string]string{"X-Bus-Signature": signPayload("secret-a", blob1)}), 200)

	var firstID string
	firstDeadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(firstDeadline) {
		evt := readNextSSEEvent(t, respObserve.Body, 2*time.Second)
		if strings.Contains(evt.Data, "\"body\":\"one\"") {
			firstID = evt.ID
			break
		}
	}
	if firstID == "" {
		t.Fatalf("did not observe first message event")
	}

	sendReq2 := map[string]any{"to": "ucla.b", "from": "ucla.a", "request_id": "rid-sse-2", "type": "request", "body": "two"}
	blob2, _ := json.Marshal(sendReq2)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq2, map[string]string{"X-Bus-Signature": signPayload("secret-a", blob2)}), 200)

	cancelObserve()
	_ = respObserve.Body.Close()

	ctxResume, cancelResume := context.WithCancel(context.Background())
	defer cancelResume()
	reqResume, _ := http.NewRequestWithContext(ctxResume, http.MethodGet, ts.URL+"/v1/observe", nil)
	reqResume.Close = true
	reqResume.Header.Set("Last-Event-ID", firstID)
	reqResume.Header.Set("X-Agent-ID", "ucla.a")
	reqResume.Header.Set("X-Bus-Signature", signPayload("secret-a", []byte(reqResume.URL.RawQuery)))
	respResume, err := c.Do(reqResume)
	if err != nil {
		t.Fatalf("open resumed observe: %v", err)
	}
	defer respResume.Body.Close()

	var resumed sseEvent
	secondDeadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(secondDeadline) {
		evt := readNextSSEEvent(t, respResume.Body, 2*time.Second)
		if strings.Contains(evt.Data, "\"body\":\"two\"") {
			resumed = evt
			break
		}
	}
	if resumed.ID == "" {
		t.Fatalf("did not observe resumed second message event")
	}
	if resumed.ID == firstID {
		t.Fatalf("expected resumed event id > %s, got same id", firstID)
	}
	if strings.Contains(resumed.Data, "rid-sse-1") {
		t.Fatalf("unexpected replay of first message in resumed stream: %s", resumed.Data)
	}
	cancelResume()
	_ = respResume.Body.Close()
}

func TestContractInjectRequiresToken(t *testing.T) {
	noTokenTS := httptest.NewServer(newContractServer())
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	mustStatus(t, doJSON(t, c, http.MethodPost, noTokenTS.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, noTokenTS.URL+"/v1/inject", map[string]any{
		"identity": "joel", "to": "ucla.b", "body": "no token configured",
	}, map[string]string{"Authorization": "Bearer " + testInjectToken}), http.StatusForbidden)
	noTokenTS.Close()

	h := newContractServerWithEnv(t, map[string]string{"INJECT_TOKENS": testInjectToken})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)

	injectReq := map[string]any{"identity": "joel", "to": "ucla.b", "body": "hello"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", injectReq, nil), http.StatusForbidden)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", injectReq, map[string]string{
		"Authorization": "Bearer " + testInjectToken,
	}), http.StatusOK)
}

func TestContractObserveRequiresTokenOrAgentHMAC(t *testing.T) {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	noTokenTS := httptest.NewServer(newContractServer())
	reqNoToken, _ := http.NewRequest(http.MethodGet, noTokenTS.URL+"/v1/observe", nil)
	respNoToken, err := c.Do(reqNoToken)
	if err != nil {
		t.Fatalf("observe no token request: %v", err)
	}
	mustStatus(t, respNoToken, http.StatusForbidden)
	noTokenTS.Close()

	h := newContractServerWithEnv(t, map[string]string{"OBSERVE_TOKENS": testObserveToken})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)

	reqDenied, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/observe", nil)
	respDenied, err := c.Do(reqDenied)
	if err != nil {
		t.Fatalf("observe denied request: %v", err)
	}
	mustStatus(t, respDenied, http.StatusForbidden)

	reqBearer, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/observe", nil)
	reqBearer.Header.Set("Authorization", "Bearer "+testObserveToken)
	respBearer, err := c.Do(reqBearer)
	if err != nil {
		t.Fatalf("observe bearer request: %v", err)
	}
	if respBearer.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respBearer.Body)
		_ = respBearer.Body.Close()
		t.Fatalf("bearer status=%d body=%s", respBearer.StatusCode, string(body))
	}
	_ = respBearer.Body.Close()

	reqQuery, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/observe?token="+testObserveToken, nil)
	respQuery, err := c.Do(reqQuery)
	if err != nil {
		t.Fatalf("observe query-token request: %v", err)
	}
	if respQuery.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respQuery.Body)
		_ = respQuery.Body.Close()
		t.Fatalf("query token status=%d body=%s", respQuery.StatusCode, string(body))
	}
	_ = respQuery.Body.Close()

	reqHMAC, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/observe?cursor=0", nil)
	reqHMAC.Header.Set("X-Agent-ID", "ucla.a")
	reqHMAC.Header.Set("X-Bus-Signature", signPayload("secret-a", []byte(reqHMAC.URL.RawQuery)))
	respHMAC, err := c.Do(reqHMAC)
	if err != nil {
		t.Fatalf("observe hmac request: %v", err)
	}
	if respHMAC.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respHMAC.Body)
		_ = respHMAC.Body.Close()
		t.Fatalf("hmac status=%d body=%s", respHMAC.StatusCode, string(body))
	}
	_ = respHMAC.Body.Close()
}

func TestContractConversationReadsRequireObserveAuth(t *testing.T) {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	h := newContractServerWithEnv(t, map[string]string{"OBSERVE_TOKENS": testObserveToken})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b",
	}, nil), http.StatusOK)

	convReq := map[string]any{
		"conversation_id": "conv-private",
		"title":           "private title",
		"participants":    []string{"ucla.a", "ucla.b"},
		"meta":            map[string]any{"sensitive": "yes"},
	}
	convBlob, _ := json.Marshal(convReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", convReq, map[string]string{
		"X-Agent-ID":      "ucla.a",
		"X-Bus-Signature": signPayload("secret-a", convBlob),
	}), http.StatusOK)

	sendReq := map[string]any{
		"to": "ucla.b", "from": "ucla.a", "conversation_id": "conv-private", "request_id": "rid-private", "type": "request", "body": "private body",
	}
	sendBlob, _ := json.Marshal(sendReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", sendBlob),
	}), http.StatusOK)

	mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, nil), http.StatusForbidden)
	mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-private/messages", nil, nil), http.StatusForbidden)

	listBody := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), http.StatusOK)
	if !bytes.Contains(listBody, []byte("private title")) || !bytes.Contains(listBody, []byte("sensitive")) {
		t.Fatalf("authorized conversation list missing private metadata: %s", string(listBody))
	}
	historyBody := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-private/messages", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), http.StatusOK)
	if !bytes.Contains(historyBody, []byte("private body")) {
		t.Fatalf("authorized conversation history missing message body: %s", string(historyBody))
	}

	listQuery := "participant=a"
	mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations?"+listQuery, nil, map[string]string{
		"X-Agent-ID":      "ucla.a",
		"X-Bus-Signature": signPayload("secret-a", []byte(listQuery)),
	}), http.StatusOK)

	historyQuery := "cursor=0"
	mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-private/messages?"+historyQuery, nil, map[string]string{
		"X-Agent-ID":      "ucla.a",
		"X-Bus-Signature": signPayload("secret-a", []byte(historyQuery)),
	}), http.StatusOK)
}

func TestContractAgentHMACConversationReadsAreScoped(t *testing.T) {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	h := newContractServerWithEnv(t, map[string]string{"OBSERVE_TOKENS": testObserveToken})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()

	for _, reg := range []map[string]any{
		{"agent_id": "ucla.reader", "capabilities": []string{"reader"}, "mode": "pull", "ttl": 60, "secret": "secret-ucla"},
		{"agent_id": "personal.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-pa"},
		{"agent_id": "personal.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-pb"},
	} {
		mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", reg, nil), http.StatusOK)
	}

	convReq := map[string]any{
		"conversation_id": "conv-personal-hidden",
		"title":           "personal hidden title",
		"participants":    []string{"personal.a", "personal.b"},
	}
	convBlob, _ := json.Marshal(convReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", convReq, map[string]string{
		"X-Agent-ID":      "personal.a",
		"X-Bus-Signature": signPayload("secret-pa", convBlob),
	}), http.StatusOK)

	sendReq := map[string]any{
		"to": "personal.b", "from": "personal.a", "conversation_id": "conv-personal-hidden", "request_id": "rid-personal-hidden", "type": "request", "body": "personal body hidden from ucla",
	}
	sendBlob, _ := json.Marshal(sendReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-pa", sendBlob),
	}), http.StatusOK)

	listQuery := ""
	uclaList := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, map[string]string{
		"X-Agent-ID":      "ucla.reader",
		"X-Bus-Signature": signPayload("secret-ucla", []byte(listQuery)),
	}), http.StatusOK)
	if bytes.Contains(uclaList, []byte("conv-personal-hidden")) || bytes.Contains(uclaList, []byte("personal hidden title")) {
		t.Fatalf("ucla HMAC saw personal conversation list data: %s", string(uclaList))
	}

	historyQuery := "cursor=0"
	mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-personal-hidden/messages?"+historyQuery, nil, map[string]string{
		"X-Agent-ID":      "ucla.reader",
		"X-Bus-Signature": signPayload("secret-ucla", []byte(historyQuery)),
	}), http.StatusNotFound)

	tokenList := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), http.StatusOK)
	if !bytes.Contains(tokenList, []byte("conv-personal-hidden")) {
		t.Fatalf("observe token should see global conversation list: %s", string(tokenList))
	}
	tokenHistory := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-personal-hidden/messages", nil, map[string]string{
		"Authorization": "Bearer " + testObserveToken,
	}), http.StatusOK)
	if !bytes.Contains(tokenHistory, []byte("personal body hidden from ucla")) {
		t.Fatalf("observe token should see global conversation history: %s", string(tokenHistory))
	}
}

func TestContractConversationCreateRequiresTokenOrAgentHMAC(t *testing.T) {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	noTokenTS := httptest.NewServer(newContractServer())
	mustStatus(t, doJSON(t, c, http.MethodPost, noTokenTS.URL+"/v1/conversations", map[string]any{
		"conversation_id": "conv-denied",
	}, nil), http.StatusForbidden)
	noTokenTS.Close()

	h := newContractServerWithEnv(t, map[string]string{"INJECT_TOKENS": testInjectToken})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a",
	}, nil), http.StatusOK)

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", map[string]any{
		"conversation_id": "conv-no-auth",
	}, nil), http.StatusForbidden)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", map[string]any{
		"conversation_id": "conv-token",
	}, map[string]string{"Authorization": "Bearer " + testInjectToken}), http.StatusOK)

	hmacReq := map[string]any{"conversation_id": "conv-hmac", "participants": []string{"ucla.a"}}
	hmacBlob, _ := json.Marshal(hmacReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", hmacReq, map[string]string{
		"X-Agent-ID":      "ucla.a",
		"X-Bus-Signature": signPayload("secret-a", hmacBlob),
	}), http.StatusOK)
}

func TestContractPushModeCallbackDelivery(t *testing.T) {
	ts := httptest.NewServer(newContractServer())
	t.Cleanup(func() { ts.CloseClientConnections() })
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	var callbackCount int32
	callbackDone := make(chan map[string]any, 1)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callbackCount, 1)
		defer r.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		select {
		case callbackDone <- payload:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.a", "mode": "pull", "capabilities": []string{"orchestrator"}, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.p", "mode": "push", "capabilities": []string{"worker"}, "callback_url": callback.URL, "secret": "secret-p",
	}, nil), http.StatusOK)

	sendReq := map[string]any{
		"to": "ucla.p", "from": "ucla.a", "request_id": "rid-push-contract", "type": "request", "body": "push me",
	}
	sendBlob, _ := json.Marshal(sendReq)
	body := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq, map[string]string{
		"X-Bus-Signature": signPayload("secret-a", sendBlob),
	}), http.StatusOK)
	var sendResp struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(body, &sendResp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if sendResp.MessageID == "" {
		t.Fatalf("expected message_id in send response")
	}

	select {
	case payload := <-callbackDone:
		gotID, _ := payload["message_id"].(string)
		if gotID != sendResp.MessageID {
			t.Fatalf("callback message_id=%q want=%q payload=%v", gotID, sendResp.MessageID, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for push callback")
	}
	if atomic.LoadInt32(&callbackCount) < 1 {
		t.Fatalf("expected callback to be invoked at least once")
	}
}

func TestContractRegisterHonorsAgentAllowlist(t *testing.T) {
	h := newContractServerWithEnv(t, map[string]string{
		"AGENT_ALLOWLIST": "ucla.alpha,ucla.beta",
	})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	bodyDenied := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "ucla.gamma", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-gamma",
	}, nil), http.StatusUnauthorized)
	if !bytes.Contains(bodyDenied, []byte("agent_id not allowlisted")) {
		t.Fatalf("expected allowlist denial, got: %s", string(bodyDenied))
	}

	bodyAllowed := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": " ucla.alpha ", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-alpha",
	}, nil), http.StatusOK)
	if !bytes.Contains(bodyAllowed, []byte(`"agent_id":"ucla.alpha"`)) {
		t.Fatalf("expected trimmed allowed agent id, got: %s", string(bodyAllowed))
	}
}

func TestContractInjectHonorsHumanAllowlist(t *testing.T) {
	h := newContractServerWithEnv(t, map[string]string{
		"HUMAN_ALLOWLIST": "joel,alex",
		"INJECT_TOKENS":   testInjectToken,
	})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	regA := map[string]any{"agent_id": "ucla.a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a"}
	regB := map[string]any{"agent_id": "ucla.b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regA, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regB, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", map[string]any{
		"conversation_id": "conv-human", "participants": []string{"ucla.a", "ucla.b"},
	}, map[string]string{"Authorization": "Bearer " + testInjectToken}), http.StatusOK)

	denied := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", map[string]any{
		"identity": "sam", "conversation_id": "conv-human", "to": "ucla.b", "body": "not allowed",
	}, map[string]string{"Authorization": "Bearer " + testInjectToken}), http.StatusUnauthorized)
	if !bytes.Contains(denied, []byte("human identity not allowed")) {
		t.Fatalf("expected human allowlist denial, got: %s", string(denied))
	}

	allowed := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", map[string]any{
		"identity": "joel", "conversation_id": "conv-human", "to": "ucla.b", "body": "allowed",
	}, map[string]string{"Authorization": "Bearer " + testInjectToken}), http.StatusOK)
	if !bytes.Contains(allowed, []byte(`"ok":true`)) {
		t.Fatalf("expected allowed inject response, got: %s", string(allowed))
	}
}

func TestBusConfigSurfaceDocumented(t *testing.T) {
	const docPath = "../../docs/BUS_HTTP_CONTRACT.md"
	blob, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	text := string(blob)
	for _, needle := range []string{
		"PORT",
		"DB_PATH",
		"STORE_BACKEND",
		"STATE_FILE",
		"ALLOWLIST_FILE",
		"AGENT_ALLOWLIST",
		"HUMAN_ALLOWLIST",
		"INJECT_TOKENS",
		"OBSERVE_TOKENS",
		"--db",
		"GracePeriod = 30s",
		"ProgressMinInterval = 2s",
		"IdempotencyWindow = 24h",
		"InboxWaitMax = 60s",
		"AckTimeout = 10s",
		"DefaultMessageTTL = 600s",
		"DefaultRegistrationTTL = 60s",
		"PushMaxAttempts = 3",
		"PushBaseBackoff = 500ms",
		"MaxInboxEventsPerAgent = 10000",
		"MaxObserveEvents = 50000",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %s to be documented in %s", needle, docPath)
		}
	}
}

// TestContractBodySizeCap pins the 413 payload_too_large behavior: unbounded
// request bodies let a single publish blow the bus past its container memory
// limit.
func TestContractBodySizeCap(t *testing.T) {
	ts := httptest.NewServer(newContractServerWithEnv(t, map[string]string{
		"MAX_BODY_BYTES": "256",
	}))
	defer ts.Close()
	c := ts.Client()

	big := strings.Repeat("x", 1024)
	resp := doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", map[string]any{
		"to": "ucla.b", "from": "ucla.a", "request_id": "r-big", "body": big,
	}, nil)
	blob := mustStatus(t, resp, 413)
	var payload struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(blob, &payload); err != nil {
		t.Fatalf("unmarshal 413 body: %v", err)
	}
	if payload.OK || payload.Error.Code != "payload_too_large" {
		t.Fatalf("expected payload_too_large error, got %s", string(blob))
	}

	// Within the cap, requests proceed to normal auth/validation handling
	// (401 here because the sender is unregistered), not 413.
	resp = doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", map[string]any{
		"to": "ucla.b", "from": "ucla.a", "request_id": "r-small", "body": "ok",
	}, nil)
	mustStatus(t, resp, 401)
}
