package bus

import (
	"strings"
)

var validScopes = map[Scope]struct{}{
	ScopePersonal: {},
	ScopeUCLA:     {},
	ScopeShared:   {},
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

func agentHasScope(agentID string, scope Scope) bool {
	for _, allowed := range agentAllowedScopes(agentID) {
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
