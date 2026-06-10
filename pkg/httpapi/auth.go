package httpapi

import (
	"fmt"
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
		secret = strings.TrimSpace(secret)
		if agentID != "" && secret != "" {
			s.agentSecrets[agentID] = secret
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
