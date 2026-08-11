package bus

import (
	"fmt"
	"strings"
)

var validScopes = map[Scope]struct{}{
	ScopePersonal: {},
	ScopeUCLA:     {},
	ScopeShared:   {},
}

var allScopeNames = []string{
	string(ScopePersonal),
	string(ScopeUCLA),
	string(ScopeShared),
}

func ScopeOfName(name string) (Scope, bool) {
	name = strings.TrimSpace(name)
	prefix, _, ok := strings.Cut(name, ".")
	if !ok || prefix == "" {
		return "", false
	}
	scope := Scope(prefix)
	_, valid := validScopes[scope]
	return scope, valid
}

func (s *Store) scopeOfName(name string) (Scope, bool) {
	if scope, ok := ScopeOfName(name); ok {
		return scope, true
	}
	if s.cfg.NamespaceMode != NamespaceModeCompat {
		return "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	return s.cfg.LegacyScope, true
}

func normalizeScopes(scopes []string) ([]string, error) {
	seen := map[Scope]struct{}{}
	out := []string{}
	for _, raw := range scopes {
		raw = strings.TrimSpace(strings.ToLower(raw))
		if raw == "" {
			continue
		}
		scope := Scope(raw)
		if _, ok := validScopes[scope]; !ok {
			return nil, newError(CodeValidation, "allowed_scopes must contain only personal, ucla, or shared", false, 0)
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, string(scope))
	}
	return out, nil
}

func normalizeSharedGrants(grants []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := []string{}
	for _, raw := range grants {
		grant := strings.TrimSpace(strings.ToLower(raw))
		switch grant {
		case "":
			continue
		case string(ScopeShared), "shared.*":
			grant = string(ScopeShared)
		default:
			return nil, newError(CodeValidation, "shared_grants must contain only shared", false, 0)
		}
		if _, ok := seen[grant]; ok {
			continue
		}
		seen[grant] = struct{}{}
		out = append(out, grant)
	}
	return out, nil
}

func agentAllowedScopes(agentID string) []string {
	scope, ok := ScopeOfName(agentID)
	if !ok {
		return nil
	}
	return []string{string(scope)}
}

func (s *Store) agentAllowedScopes(agentID string) []string {
	if s.isControlPlaneAgent(agentID) {
		return cloneStrings(allScopeNames)
	}
	scope, ok := s.scopeOfName(agentID)
	if !ok {
		return nil
	}
	return []string{string(scope)}
}

// NormalizeControlPlaneAgents validates the shared configuration contract and
// returns unique identities in declaration order.
func NormalizeControlPlaneAgents(agents []string) ([]string, error) {
	seen := map[string]struct{}{}
	normalized := []string{}
	for _, raw := range agents {
		agentID := strings.TrimSpace(raw)
		if agentID == "" {
			continue
		}
		if strings.Contains(agentID, ".") {
			return nil, fmt.Errorf("control-plane identity %q must be unprefixed", agentID)
		}
		if _, ok := seen[agentID]; ok {
			continue
		}
		seen[agentID] = struct{}{}
		normalized = append(normalized, agentID)
	}
	return normalized, nil
}

func (s *Store) isConfiguredControlPlaneAgent(agentID string) bool {
	_, ok := s.controlPlaneAgents[strings.TrimSpace(agentID)]
	return ok
}

func (s *Store) isControlPlaneAgent(agentID string) bool {
	return s.cfg.NamespaceMode == NamespaceModeStrict && s.isConfiguredControlPlaneAgent(agentID)
}

func (s *Store) acceptsName(name string) bool {
	if s.isControlPlaneAgent(name) {
		return true
	}
	_, ok := s.scopeOfName(name)
	return ok
}

func (s *Store) agentHasScope(agentID string, scope Scope) bool {
	for _, allowed := range s.agentAllowedScopes(agentID) {
		if allowed == string(scope) {
			return true
		}
	}
	return false
}

func (s *Store) agentSharedGrants(agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	if s.isControlPlaneAgent(agentID) {
		return []string{string(ScopeShared)}
	}
	for _, raw := range s.cfg.SharedGrantAgents {
		allowed := strings.TrimSpace(raw)
		if allowed == agentID {
			return []string{string(ScopeShared)}
		}
	}
	return nil
}

func (s *Store) agentHasSharedGrant(agentID string) bool {
	for _, grant := range s.agentSharedGrants(agentID) {
		if grant == string(ScopeShared) {
			return true
		}
	}
	return false
}
