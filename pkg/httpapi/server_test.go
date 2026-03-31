package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func newServerForTest(t *testing.T) *Server {
	t.Helper()
	return newServerForTestWithEnv(t, nil)
}

func newServerForTestWithEnv(t *testing.T, env map[string]string) *Server {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
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
	server, err := NewServerFromEnv(store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server
}

func writeAllowlistFileAtomically(t *testing.T, path string, lines ...string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp allowlist: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename allowlist into place: %v", err)
	}
}

func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func postJSON(t *testing.T, h http.Handler, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func getWithHeaders(t *testing.T, h http.Handler, rawPath string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawPath, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func mustRegisterAgent(t *testing.T, h http.Handler, agentID, secret string) {
	t.Helper()
	rr := postJSON(t, h, "/v1/agents/register", map[string]any{
		"agent_id": agentID, "mode": "pull", "capabilities": []string{"x"}, "secret": secret,
	}, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("register %s status=%d body=%s", agentID, rr.Code, rr.Body.String())
	}
}

func mustSendMessage(t *testing.T, h http.Handler, fromSecret string, body map[string]any) string {
	t.Helper()
	blob, _ := json.Marshal(body)
	rr := postJSON(t, h, "/v1/messages", body, map[string]string{"X-Bus-Signature": sign(fromSecret, blob)})
	if rr.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if strings.TrimSpace(out.MessageID) == "" {
		t.Fatalf("missing message_id in send response")
	}
	return out.MessageID
}

func TestEventsRequiresAuthAndActorHeader(t *testing.T) {
	h := newServerForTest(t)

	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-http", "type": "request", "body": "do",
	}
	messageID := mustSendMessage(t, h, "secret-a", sendBody)

	eventBody := map[string]any{
		"message_id": messageID,
		"type":       "progress",
		"body":       "10%",
	}
	blobEvent, _ := json.Marshal(eventBody)

	rrEvent := postJSON(t, h, "/v1/events", eventBody, nil)
	if rrEvent.Code != 401 {
		t.Fatalf("expected 401, got %d body=%s", rrEvent.Code, rrEvent.Body.String())
	}

	rrEvent2 := postJSON(t, h, "/v1/events", eventBody, map[string]string{"X-Agent-ID": "b", "X-Bus-Signature": sign("secret-b", blobEvent)})
	if rrEvent2.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rrEvent2.Code, rrEvent2.Body.String())
	}
}

func TestRegisterTrimsAgentIDBeforeAllowlistAndSecretLookup(t *testing.T) {
	h := newServerForTestWithEnv(t, map[string]string{"AGENT_ALLOWLIST": "a,b"})

	mustRegisterAgent(t, h, "  a  ", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	// If registration/secret keying is not normalized, this signed request fails.
	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-trim", "type": "request", "body": "ok",
	}
	blob, _ := json.Marshal(sendBody)
	rr := postJSON(t, h, "/v1/messages", sendBody, map[string]string{"X-Bus-Signature": sign("secret-a", blob)})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMessagesRejectsTamperedSignature(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-badsig", "type": "request", "body": "do",
	}
	blob, _ := json.Marshal(sendBody)
	goodSig := sign("secret-a", blob)
	badSig := "00" + goodSig[2:]

	rr := postJSON(t, h, "/v1/messages", sendBody, map[string]string{"X-Bus-Signature": badSig})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMessagesAcceptsSHA256PrefixedSignature(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-prefix", "type": "request", "body": "do",
	}
	blob, _ := json.Marshal(sendBody)
	rr := postJSON(t, h, "/v1/messages", sendBody, map[string]string{"X-Bus-Signature": "sha256=" + sign("secret-a", blob)})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMessagesRejectsNonHexSignature(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-nonhex", "type": "request", "body": "do",
	}
	rr := postJSON(t, h, "/v1/messages", sendBody, map[string]string{"X-Bus-Signature": "not-hex"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEventsRejectActorThatDoesNotOwnMessage(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")
	mustRegisterAgent(t, h, "c", "secret-c")

	messageID := mustSendMessage(t, h, "secret-a", map[string]any{
		"to": "b", "from": "a", "request_id": "rid-owner", "type": "request", "body": "job",
	})
	eventBody := map[string]any{
		"message_id": messageID,
		"type":       "progress",
		"body":       "not allowed",
	}
	blob, _ := json.Marshal(eventBody)
	rr := postJSON(t, h, "/v1/events", eventBody, map[string]string{
		"X-Agent-ID":      "c",
		"X-Bus-Signature": sign("secret-c", blob),
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInboxRejectsSignatureForDifferentRawQuery(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")
	mustRegisterAgent(t, h, "b", "secret-b")

	mustSendMessage(t, h, "secret-a", map[string]any{
		"to": "b", "from": "a", "request_id": "rid-inbox", "type": "request", "body": "payload",
	})

	goodQuery := url.Values{
		"agent_id": []string{"b"},
		"cursor":   []string{"0"},
		"wait":     []string{"0"},
	}.Encode()
	sig := sign("secret-b", []byte(goodQuery))

	// Different raw query ordering should not validate.
	tamperedRawQuery := "cursor=0&wait=0&agent_id=b"
	rr := getWithHeaders(t, h, "/v1/inbox?"+tamperedRawQuery, map[string]string{"X-Bus-Signature": sig})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTopLevelHealthMatchesV1Health(t *testing.T) {
	h := newServerForTest(t)

	mustRegisterAgent(t, h, "a", "secret-a")
	rrTop := getWithHeaders(t, h, "/health", nil)
	rrV1 := getWithHeaders(t, h, "/v1/health", nil)
	if rrTop.Code != http.StatusOK || rrV1.Code != http.StatusOK {
		t.Fatalf("unexpected status codes top=%d v1=%d", rrTop.Code, rrV1.Code)
	}
	if strings.TrimSpace(rrTop.Body.String()) != strings.TrimSpace(rrV1.Body.String()) {
		t.Fatalf("expected top-level health to match v1 health\ntop=%s\nv1=%s", rrTop.Body.String(), rrV1.Body.String())
	}
}

func TestMetricsExposesPrometheusText(t *testing.T) {
	h := newServerForTest(t)
	mustRegisterAgent(t, h, "a", "secret-a")

	rr := getWithHeaders(t, h, "/metrics", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, needle := range []string{
		"# HELP agent_bus_agents_active",
		"agent_bus_agents_active 1",
		"agent_bus_push_successes_total",
		"agent_bus_push_failures_total",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected %q in metrics output:\n%s", needle, body)
		}
	}
}

func TestNewServerFromEnvFailsWhenAllowlistFileMissing(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-allowlist.txt")
	t.Setenv("ALLOWLIST_FILE", missingPath)

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

	_, err := NewServerFromEnv(store)
	if err == nil {
		t.Fatalf("expected missing allowlist file error")
	}
	if !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("expected error to mention path, got %v", err)
	}
}

func TestNewServerFromEnvFallsBackToEnvAllowlistWhenFileUnset(t *testing.T) {
	h := newServerForTestWithEnv(t, map[string]string{"AGENT_ALLOWLIST": "alpha,beta"})

	denied := postJSON(t, h, "/v1/agents/register", map[string]any{
		"agent_id": "gamma", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-gamma",
	}, nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", denied.Code, denied.Body.String())
	}

	allowed := postJSON(t, h, "/v1/agents/register", map[string]any{
		"agent_id": "alpha", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-alpha",
	}, nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestAllowlistWatcherReloadsAtomicRenameAndPreservesLiveAgent(t *testing.T) {
	dir := t.TempDir()
	allowlistPath := filepath.Join(dir, "allowlist.txt")
	writeAllowlistFileAtomically(t, allowlistPath, "# comment", "a", "b")

	server := newServerForTestWithEnv(t, map[string]string{"ALLOWLIST_FILE": allowlistPath})
	var logBuf bytes.Buffer
	server.logger = log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.WatchAllowlistFile(ctx, allowlistPath); err != nil {
		t.Fatalf("watch allowlist: %v", err)
	}

	mustRegisterAgent(t, server, "a", "secret-a")
	mustRegisterAgent(t, server, "b", "secret-b")

	writeAllowlistFileAtomically(t, allowlistPath, "b", "c")
	waitFor(t, "allowlist add/remove reload", func() bool {
		return server.isAgentAllowed("c") && !server.isAgentAllowed("a")
	})

	rrReRegister := postJSON(t, server, "/v1/agents/register", map[string]any{
		"agent_id": "a", "mode": "pull", "capabilities": []string{"x"}, "secret": "secret-a-2",
	}, nil)
	if rrReRegister.Code != http.StatusUnauthorized {
		t.Fatalf("expected re-register denial after removal, got %d body=%s", rrReRegister.Code, rrReRegister.Body.String())
	}

	sendBody := map[string]any{
		"to": "b", "from": "a", "request_id": "rid-live-agent", "type": "request", "body": "still works",
	}
	blob, _ := json.Marshal(sendBody)
	rrSend := postJSON(t, server, "/v1/messages", sendBody, map[string]string{"X-Bus-Signature": sign("secret-a", blob)})
	if rrSend.Code != http.StatusOK {
		t.Fatalf("expected existing agent to keep working, got %d body=%s", rrSend.Code, rrSend.Body.String())
	}

	if !strings.Contains(logBuf.String(), "allowlist reloaded") {
		t.Fatalf("expected reload log, got %q", logBuf.String())
	}
}

func TestAllowlistWatcherKeepsLastGoodSetOnReloadError(t *testing.T) {
	dir := t.TempDir()
	allowlistPath := filepath.Join(dir, "allowlist.txt")
	writeAllowlistFileAtomically(t, allowlistPath, "a", "b")

	server := newServerForTestWithEnv(t, map[string]string{"ALLOWLIST_FILE": allowlistPath})
	var logBuf bytes.Buffer
	server.logger = log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.WatchAllowlistFile(ctx, allowlistPath); err != nil {
		t.Fatalf("watch allowlist: %v", err)
	}

	brokenPath := filepath.Join(dir, "allowlist.bak")
	if err := os.Rename(allowlistPath, brokenPath); err != nil {
		t.Fatalf("rename allowlist away: %v", err)
	}

	waitFor(t, "reload error log", func() bool {
		return strings.Contains(logBuf.String(), "allowlist reload failed")
	})
	if !server.isAgentAllowed("a") || !server.isAgentAllowed("b") {
		t.Fatalf("expected last-good allowset to remain active after reload failure")
	}
}
