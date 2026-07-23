package bus

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func newScopeTestStore(logBuf *bytes.Buffer, sharedGrantAgents ...string) *Store {
	logger := log.New(logBuf, "", 0)
	return NewStore(Config{
		Clock: func() time.Time {
			return time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
		},
		Logger:            logger,
		SharedGrantAgents: sharedGrantAgents,
	})
}

func mustRegisterScoped(t *testing.T, s *Store, agentID string, scopes, grants []string) {
	t.Helper()
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID:       agentID,
		AllowedScopes: scopes,
		SharedGrants:  grants,
		Mode:          AgentModePull,
		TTLSeconds:    60,
	}); err != nil {
		t.Fatalf("register %s: %v", agentID, err)
	}
}

func TestScopeMatrixForPublish(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		scopes  []string
		grants  []string
		policy  []string
		to      string
		wantErr bool
	}{
		{name: "personal to personal", from: "personal.sender", scopes: []string{"personal"}, to: "personal.target"},
		{name: "personal to ucla denied", from: "personal.sender", scopes: []string{"personal"}, to: "ucla.target", wantErr: true},
		{name: "ucla to ucla", from: "ucla.sender", scopes: []string{"ucla"}, to: "ucla.target"},
		{name: "ucla to personal denied", from: "ucla.sender", scopes: []string{"ucla"}, to: "personal.target", wantErr: true},
		{name: "ucla with shared membership denied", from: "ucla.sender", scopes: []string{"ucla", "shared"}, policy: []string{"shared.target"}, to: "shared.target", wantErr: true},
		{name: "ucla with server shared grant allowed", from: "ucla.sender", scopes: []string{"ucla"}, grants: []string{"shared"}, policy: []string{"ucla.sender", "shared.target"}, to: "shared.target"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := newScopeTestStore(&logs, tc.policy...)
			mustRegisterScoped(t, s, tc.from, tc.scopes, tc.grants)
			targetScope, _ := ScopeOfName(tc.to)
			mustRegisterScoped(t, s, tc.to, []string{string(targetScope)}, nil)

			_, _, err := s.SendMessage(SendMessageInput{
				From:      tc.from,
				To:        tc.to,
				RequestID: "rid",
				Type:      MessageTypeRequest,
				Body:      "hello",
			})
			if tc.wantErr && err == nil {
				t.Fatalf("expected send error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("send: %v", err)
			}
		})
	}
}

func TestUCLAPublishToPersonalDeniedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.sender", []string{"ucla"}, nil)
	mustRegisterScoped(t, s, "personal.target", []string{"personal"}, nil)

	_, _, err := s.SendMessage(SendMessageInput{
		From:      "ucla.sender",
		To:        "personal.target",
		RequestID: "rid-deny",
		Type:      MessageTypeRequest,
		Body:      "nope",
	})
	if err == nil {
		t.Fatalf("expected scope denial")
	}
	if !strings.Contains(logs.String(), "scope denied") || !strings.Contains(logs.String(), "identity=ucla.sender") || !strings.Contains(logs.String(), "resource=personal.target") {
		t.Fatalf("expected scope denial log, got %q", logs.String())
	}
}

func TestUnprefixedQueueNamesRejected(t *testing.T) {
	var logs bytes.Buffer
	s := NewStore(Config{
		NamespaceMode: NamespaceModeStrict,
		Logger:        log.New(&logs, "", 0),
		Clock: func() time.Time {
			return time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "worker", Mode: AgentModePull}); err == nil {
		t.Fatalf("expected unprefixed registration to fail")
	}
	mustRegisterScoped(t, s, "ucla.sender", []string{"ucla"}, nil)
	if _, _, err := s.SendMessage(SendMessageInput{
		From:      "ucla.sender",
		To:        "target",
		RequestID: "rid-unprefixed",
		Type:      MessageTypeRequest,
		Body:      "hello",
	}); err == nil {
		t.Fatalf("expected unprefixed target to fail")
	}
}

func TestCompatModeAcceptsLegacyIDsForRegisterSendAndPoll(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "legacy-sender", nil, nil)
	mustRegisterScoped(t, s, "legacy-target", nil, nil)

	agents := s.ListAgents("")
	for _, agent := range agents {
		if strings.HasPrefix(agent.AgentID, "legacy-") {
			if got := strings.Join(agent.AllowedScopes, ","); got != string(ScopeUCLA) {
				t.Fatalf("legacy agent %s effective scope=%q want ucla", agent.AgentID, got)
			}
		}
	}

	msg, _, err := s.SendMessage(SendMessageInput{
		From:      "legacy-sender",
		To:        "legacy-target",
		RequestID: "rid-legacy",
		Type:      MessageTypeRequest,
		Body:      "compat works",
	})
	if err != nil {
		t.Fatalf("send legacy message in compat mode: %v", err)
	}
	events, _, err := s.PollInbox(PollInboxInput{AgentID: "legacy-target", Cursor: 0, Wait: 0})
	if err != nil {
		t.Fatalf("poll legacy inbox in compat mode: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != msg.MessageID {
		t.Fatalf("legacy inbox event mismatch: %#v", events)
	}
}

func TestStrictModeRejectsLegacyIDs(t *testing.T) {
	var logs bytes.Buffer
	s := NewStore(Config{
		NamespaceMode: NamespaceModeStrict,
		Logger:        log.New(&logs, "", 0),
		Clock: func() time.Time {
			return time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
		},
	})

	if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: "legacy-agent", Mode: AgentModePull}); err == nil {
		t.Fatalf("expected strict mode to reject unprefixed registration")
	}
	mustRegisterScoped(t, s, "ucla.sender", nil, nil)
	if _, _, err := s.SendMessage(SendMessageInput{
		From:      "ucla.sender",
		To:        "legacy-target",
		RequestID: "rid-strict-send",
		Type:      MessageTypeRequest,
		Body:      "blocked",
	}); err == nil {
		t.Fatalf("expected strict mode to reject unprefixed send target")
	}
	if _, _, err := s.PollInbox(PollInboxInput{AgentID: "legacy-target", Cursor: 0, Wait: 0}); err == nil {
		t.Fatalf("expected strict mode to reject unprefixed inbox")
	}
}

func TestCompatLegacyIDCannotCrossScope(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "legacy-sender", []string{"personal"}, nil)
	mustRegisterScoped(t, s, "personal.target", nil, nil)

	_, _, err := s.SendMessage(SendMessageInput{
		From:      "legacy-sender",
		To:        "personal.target",
		RequestID: "rid-legacy-cross",
		Type:      MessageTypeRequest,
		Body:      "blocked",
	})
	if err == nil {
		t.Fatalf("expected legacy ucla identity to be denied personal publish")
	}
	if !strings.Contains(logs.String(), "identity=legacy-sender") || !strings.Contains(logs.String(), "resource=personal.target") {
		t.Fatalf("expected legacy cross-scope denial log, got %q", logs.String())
	}
}

func TestSharedRequiresExplicitGrantForSubscribe(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	if _, err := s.RegisterAgent(RegisterAgentInput{
		AgentID:       "shared.worker",
		AllowedScopes: []string{"shared"},
		SharedGrants:  []string{"shared"},
		Mode:          AgentModePull,
		TTLSeconds:    60,
	}); err == nil {
		t.Fatalf("expected shared registration without grant to fail")
	}
	if !strings.Contains(logs.String(), "shared grant required") {
		t.Fatalf("expected shared grant denial log, got %q", logs.String())
	}
	s = newScopeTestStore(&logs, "shared.worker")
	mustRegisterScoped(t, s, "shared.worker", []string{"shared"}, []string{"shared"})
}

func TestRegistrationClaimsCannotSelfEscalateScopes(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.sender", []string{"ucla", "personal"}, nil)
	mustRegisterScoped(t, s, "personal.target", []string{"personal"}, nil)

	agent := s.ListAgents("")[1]
	if agent.AgentID != "ucla.sender" {
		t.Fatalf("expected ucla.sender to sort second, got %s", agent.AgentID)
	}
	if got := strings.Join(agent.AllowedScopes, ","); got != "ucla" {
		t.Fatalf("registration claim changed effective scopes: %q", got)
	}

	_, _, err := s.SendMessage(SendMessageInput{
		From:      "ucla.sender",
		To:        "personal.target",
		RequestID: "rid-self-scope",
		Type:      MessageTypeRequest,
		Body:      "no escalation",
	})
	if err == nil {
		t.Fatalf("expected self-claimed personal scope to be denied")
	}
}

func TestRegistrationClaimsCannotSelfGrantSharedAccess(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs, "shared.target")
	mustRegisterScoped(t, s, "ucla.sender", []string{"ucla"}, []string{"shared"})
	mustRegisterScoped(t, s, "shared.target", []string{"shared"}, nil)

	agents := s.ListAgents("")
	for _, agent := range agents {
		if agent.AgentID == "ucla.sender" && len(agent.SharedGrants) != 0 {
			t.Fatalf("registration claim changed effective shared grants: %#v", agent.SharedGrants)
		}
	}

	_, _, err := s.SendMessage(SendMessageInput{
		From:      "ucla.sender",
		To:        "shared.target",
		RequestID: "rid-self-shared",
		Type:      MessageTypeRequest,
		Body:      "no shared escalation",
	})
	if err == nil {
		t.Fatalf("expected self-claimed shared grant to be denied")
	}
}
