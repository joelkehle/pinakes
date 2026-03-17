package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func newServerForTest() http.Handler {
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
	h := newServerForTest()

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
	t.Setenv("AGENT_ALLOWLIST", "a,b")
	h := newServerForTest()

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
	h := newServerForTest()
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
	h := newServerForTest()
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
	h := newServerForTest()
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
	h := newServerForTest()
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
	h := newServerForTest()
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
	h := newServerForTest()

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
	h := newServerForTest()
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
