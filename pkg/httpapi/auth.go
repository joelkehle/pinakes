package httpapi

import (
	"crypto/hmac"
	"fmt"
	"net/http"
	"strings"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func parseTokenEnv(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, entry := range strings.Split(raw, ",") {
		token := strings.TrimSpace(entry)
		if token != "" {
			out[token] = struct{}{}
		}
	}
	return out
}

func bearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(strings.TrimSpace(scheme), "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func tokenAllowed(allowset map[string]struct{}, token string) bool {
	token = strings.TrimSpace(token)
	if len(allowset) == 0 || token == "" {
		return false
	}
	for allowed := range allowset {
		if hmac.Equal([]byte(token), []byte(allowed)) {
			return true
		}
	}
	return false
}

func forbiddenError(message string) *bus.Error {
	return &bus.Error{Code: bus.CodeUnauthorized, Message: message, Status: http.StatusForbidden}
}

func (s *Server) loadPersistedAgentSecrets() error {
	secretStore, ok := s.store.(bus.AgentSecretStore)
	if !ok {
		return nil
	}
	secrets, err := secretStore.AgentSecrets()
	if err != nil {
		return fmt.Errorf("load agent secrets: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for agentID, secret := range secrets {
		agentID = strings.TrimSpace(agentID)
		if agentID != "" && strings.TrimSpace(secret) != "" {
			s.agentSecrets[agentID] = secret
		}
	}
	return nil
}

func (s *Server) currentAgentSecret(agentID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	secret, ok := s.agentSecrets[strings.TrimSpace(agentID)]
	return secret, ok
}

func (s *Server) verifyReregistrationProof(agentID, offeredSecret string, payload []byte, signature string) error {
	currentSecret, hasSecret := s.currentAgentSecret(agentID)
	if !hasSecret {
		// Unknown agents and legacy pre-persistence rows with no stored secret
		// are allowed to establish a secret after the allowlist check.
		return nil
	}
	if strings.TrimSpace(currentSecret) == "" || currentSecret == offeredSecret {
		return nil
	}
	if err := s.verifySignature(agentID, signature, payload); err != nil {
		return &bus.Error{
			Code:    bus.CodeRejected,
			Message: "re-registration requires proof of current secret",
			Status:  http.StatusConflict,
		}
	}
	return nil
}

func (s *Server) validInjectToken(r *http.Request) bool {
	return tokenAllowed(s.injectTokens, bearerToken(r))
}

func (s *Server) validObserveToken(r *http.Request) bool {
	if tokenAllowed(s.observeTokens, bearerToken(r)) {
		return true
	}
	return tokenAllowed(s.observeTokens, r.URL.Query().Get("token"))
}

func (s *Server) verifyObserveAuth(r *http.Request) error {
	if s.validObserveToken(r) {
		return nil
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	signature := strings.TrimSpace(r.Header.Get("X-Bus-Signature"))
	if agentID == "" && signature == "" {
		return forbiddenError("observe auth required")
	}
	return s.verifySignature(agentID, signature, []byte(r.URL.RawQuery))
}

func (s *Server) verifyConversationCreateAuth(r *http.Request, payload []byte) error {
	if s.validInjectToken(r) {
		return nil
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	signature := strings.TrimSpace(r.Header.Get("X-Bus-Signature"))
	if agentID == "" && signature == "" {
		return forbiddenError("conversation create auth required")
	}
	return s.verifySignature(agentID, signature, payload)
}

func (s *Server) setAgentSecret(agentID, secret string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || strings.TrimSpace(secret) == "" {
		return nil
	}
	if secretStore, ok := s.store.(bus.AgentSecretStore); ok {
		if err := secretStore.SetAgentSecret(agentID, secret); err != nil {
			return fmt.Errorf("persist agent secret: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSecrets[agentID] = secret
	return nil
}
