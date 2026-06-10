package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/joelkehle/pinakes/pkg/bus"
)

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
