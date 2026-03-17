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

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func newContractServer() http.Handler {
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	store := bus.NewStore(bus.Config{
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
	})
	return NewServer(store)
}

func newContractServerPersistent(t *testing.T) http.Handler {
	t.Helper()
	now := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	statePath := t.TempDir() + "/state.json"
	store, err := bus.NewPersistentStore(statePath, bus.Config{
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
	})
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

	regA := map[string]any{"agent_id": "a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a"}
	regB := map[string]any{"agent_id": "b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regA, nil), 200)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regB, nil), 200)

	blobAgents := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/agents", nil, nil), 200)
	if !bytes.Contains(blobAgents, []byte("\"agent_id\":\"a\"")) || !bytes.Contains(blobAgents, []byte("\"agent_id\":\"b\"")) {
		t.Fatalf("expected agents list to include a and b: %s", string(blobAgents))
	}

	convReq := map[string]any{"conversation_id": "conv-1", "title": "test", "participants": []string{"a", "b"}, "meta": map[string]any{"case": "c1"}}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", convReq, nil), 200)
	blobConvs := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations", nil, nil), 200)
	if !bytes.Contains(blobConvs, []byte("conv-1")) {
		t.Fatalf("expected conversation listing to include conv-1: %s", string(blobConvs))
	}

	sendReq := map[string]any{
		"to": "b", "from": "a", "conversation_id": "conv-1", "request_id": "rid-1", "type": "request", "body": "do work",
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

	query := "agent_id=b&cursor=0&wait=0"
	respInbox := doJSON(t, c, http.MethodGet, ts.URL+"/v1/inbox?"+query, nil, map[string]string{"X-Bus-Signature": signPayload("secret-b", []byte(query))})
	blobInbox := mustStatus(t, respInbox, 200)
	if !bytes.Contains(blobInbox, []byte(sendResp.MessageID)) {
		t.Fatalf("expected inbox to include message: %s", string(blobInbox))
	}

	ackReq := map[string]any{"agent_id": "b", "message_id": sendResp.MessageID, "status": "accepted"}
	ackBlob, _ := json.Marshal(ackReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/acks", ackReq, map[string]string{"X-Bus-Signature": signPayload("secret-b", ackBlob)}), 200)

	evtReq := map[string]any{"message_id": sendResp.MessageID, "type": "progress", "body": "50%"}
	evtBlob, _ := json.Marshal(evtReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/events", evtReq, map[string]string{"X-Agent-ID": "b", "X-Bus-Signature": signPayload("secret-b", evtBlob)}), 200)

	evtFinalReq := map[string]any{"message_id": sendResp.MessageID, "type": "final", "body": "done"}
	evtFinalBlob, _ := json.Marshal(evtFinalReq)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/events", evtFinalReq, map[string]string{"X-Agent-ID": "b", "X-Bus-Signature": signPayload("secret-b", evtFinalBlob)}), 200)

	blobHistory := mustStatus(t, doJSON(t, c, http.MethodGet, ts.URL+"/v1/conversations/conv-1/messages", nil, nil), 200)
	if !bytes.Contains(blobHistory, []byte(sendResp.MessageID)) {
		t.Fatalf("expected history to include message: %s", string(blobHistory))
	}

	injectReq := map[string]any{"identity": "joel", "conversation_id": "conv-1", "to": "b", "body": "human note"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", injectReq, nil), 200)

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
	runContractAllEndpoints(t, newContractServer())
}

func TestContractAllEndpointsPersistentBackend(t *testing.T) {
	runContractAllEndpoints(t, newContractServerPersistent(t))
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

	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{"agent_id": "a", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-a"}, nil), 200)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{"agent_id": "b", "mode": "pull", "capabilities": []string{"y"}, "secret": "secret-b"}, nil), 200)

	ctxObserve, cancelObserve := context.WithCancel(context.Background())
	defer cancelObserve()
	reqObserve, _ := http.NewRequestWithContext(ctxObserve, http.MethodGet, ts.URL+"/v1/observe", nil)
	reqObserve.Close = true
	respObserve, err := c.Do(reqObserve)
	if err != nil {
		t.Fatalf("open observe: %v", err)
	}
	defer respObserve.Body.Close()

	sendReq1 := map[string]any{"to": "b", "from": "a", "request_id": "rid-sse-1", "type": "request", "body": "one"}
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

	sendReq2 := map[string]any{"to": "b", "from": "a", "request_id": "rid-sse-2", "type": "request", "body": "two"}
	blob2, _ := json.Marshal(sendReq2)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/messages", sendReq2, map[string]string{"X-Bus-Signature": signPayload("secret-a", blob2)}), 200)

	cancelObserve()
	_ = respObserve.Body.Close()

	ctxResume, cancelResume := context.WithCancel(context.Background())
	defer cancelResume()
	reqResume, _ := http.NewRequestWithContext(ctxResume, http.MethodGet, ts.URL+"/v1/observe", nil)
	reqResume.Close = true
	reqResume.Header.Set("Last-Event-ID", firstID)
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
		"agent_id": "a", "mode": "pull", "capabilities": []string{"orchestrator"}, "secret": "secret-a",
	}, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": "p", "mode": "push", "capabilities": []string{"worker"}, "callback_url": callback.URL, "secret": "secret-p",
	}, nil), http.StatusOK)

	sendReq := map[string]any{
		"to": "p", "from": "a", "request_id": "rid-push-contract", "type": "request", "body": "push me",
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
		"AGENT_ALLOWLIST": "alpha,beta",
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
		"agent_id": "gamma", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-gamma",
	}, nil), http.StatusUnauthorized)
	if !bytes.Contains(bodyDenied, []byte("agent_id not allowlisted")) {
		t.Fatalf("expected allowlist denial, got: %s", string(bodyDenied))
	}

	bodyAllowed := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", map[string]any{
		"agent_id": " alpha ", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-alpha",
	}, nil), http.StatusOK)
	if !bytes.Contains(bodyAllowed, []byte(`"agent_id":"alpha"`)) {
		t.Fatalf("expected trimmed allowed agent id, got: %s", string(bodyAllowed))
	}
}

func TestContractInjectHonorsHumanAllowlist(t *testing.T) {
	h := newContractServerWithEnv(t, map[string]string{
		"HUMAN_ALLOWLIST": "joel,alex",
	})
	ts := httptest.NewServer(h)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()
	c := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	regA := map[string]any{"agent_id": "a", "capabilities": []string{"orchestrator"}, "mode": "pull", "ttl": 60, "secret": "secret-a"}
	regB := map[string]any{"agent_id": "b", "capabilities": []string{"worker"}, "mode": "pull", "ttl": 60, "secret": "secret-b"}
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regA, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/agents/register", regB, nil), http.StatusOK)
	mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/conversations", map[string]any{
		"conversation_id": "conv-human", "participants": []string{"a", "b"},
	}, nil), http.StatusOK)

	denied := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", map[string]any{
		"identity": "sam", "conversation_id": "conv-human", "to": "b", "body": "not allowed",
	}, nil), http.StatusUnauthorized)
	if !bytes.Contains(denied, []byte("human identity not allowed")) {
		t.Fatalf("expected human allowlist denial, got: %s", string(denied))
	}

	allowed := mustStatus(t, doJSON(t, c, http.MethodPost, ts.URL+"/v1/inject", map[string]any{
		"identity": "joel", "conversation_id": "conv-human", "to": "b", "body": "allowed",
	}, nil), http.StatusOK)
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
		"AGENT_ALLOWLIST",
		"HUMAN_ALLOWLIST",
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
