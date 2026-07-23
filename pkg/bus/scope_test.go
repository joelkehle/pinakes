package bus

import (
	"bytes"
	"fmt"
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

func TestObserveFilterHidesPersonalEventsFromUCLAActor(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.reader", nil, nil)
	mustRegisterScoped(t, s, "personal.sender", nil, nil)
	mustRegisterScoped(t, s, "personal.target", nil, nil)

	_, _, err := s.SendMessage(SendMessageInput{
		From:      "personal.sender",
		To:        "personal.target",
		RequestID: "rid-personal-observe",
		Type:      MessageTypeRequest,
		Body:      "personal body hidden from ucla",
	})
	if err != nil {
		t.Fatalf("send personal message: %v", err)
	}

	uclaEvents, _ := s.ObserveSince(0, ObserveFilter{ActorAgentID: "ucla.reader"}, 0)
	for _, evt := range uclaEvents {
		if strings.Contains(fmt.Sprint(evt.Data), "personal body hidden from ucla") {
			t.Fatalf("ucla actor observed personal event: %#v", evt)
		}
	}

	globalEvents, _ := s.ObserveSince(0, ObserveFilter{}, 0)
	found := false
	for _, evt := range globalEvents {
		if strings.Contains(fmt.Sprint(evt.Data), "personal body hidden from ucla") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("global observe did not include personal event")
	}
}

func TestMixedUCLAPersonalConversationHiddenFromUCLAReader(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.reader", nil, nil)

	conv, err := s.CreateConversation(CreateConversationInput{
		ConversationID: "conv-mixed-ucla-personal",
		Title:          "mixed personal title",
		Participants:   []string{"ucla.peer", "personal.peer"},
	})
	if err != nil {
		t.Fatalf("global create mixed ucla/personal conversation: %v", err)
	}
	if _, err := s.Inject(InjectInput{
		Identity:       "operator",
		ConversationID: conv.ConversationID,
		Body:           "mixed personal history body",
	}); err != nil {
		t.Fatalf("inject mixed personal history: %v", err)
	}

	uclaConvs := s.ListConversations(ListConversationsFilter{ActorAgentID: "ucla.reader"})
	for _, got := range uclaConvs {
		if got.ConversationID == conv.ConversationID || strings.Contains(got.Title, "mixed personal") {
			t.Fatalf("ucla reader saw mixed ucla/personal conversation: %#v", got)
		}
	}
	if _, _, _, err := s.ListConversationMessages(ListConversationMessagesInput{
		ConversationID: conv.ConversationID,
		ActorAgentID:   "ucla.reader",
	}); err == nil {
		t.Fatalf("expected ucla reader history lookup to be hidden")
	}

	_, globalMessages, _, err := s.ListConversationMessages(ListConversationMessagesInput{
		ConversationID: conv.ConversationID,
	})
	if err != nil {
		t.Fatalf("global history lookup: %v", err)
	}
	if len(globalMessages) != 1 || !strings.Contains(globalMessages[0].Body, "mixed personal history body") {
		t.Fatalf("global history missing mixed personal body: %#v", globalMessages)
	}
}

func TestMixedUCLASharedConversationHiddenWithoutGrant(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.reader", nil, nil)

	conv, err := s.CreateConversation(CreateConversationInput{
		ConversationID: "conv-mixed-ucla-shared",
		Title:          "mixed shared title",
		Participants:   []string{"ucla.peer", "shared.room"},
	})
	if err != nil {
		t.Fatalf("global create mixed ucla/shared conversation: %v", err)
	}
	if _, err := s.Inject(InjectInput{
		Identity:       "operator",
		ConversationID: conv.ConversationID,
		Body:           "mixed shared history body",
	}); err != nil {
		t.Fatalf("inject mixed shared history: %v", err)
	}

	uclaConvs := s.ListConversations(ListConversationsFilter{ActorAgentID: "ucla.reader"})
	for _, got := range uclaConvs {
		if got.ConversationID == conv.ConversationID || strings.Contains(got.Title, "mixed shared") {
			t.Fatalf("ungranted ucla reader saw mixed ucla/shared conversation: %#v", got)
		}
	}
	if _, _, _, err := s.ListConversationMessages(ListConversationMessagesInput{
		ConversationID: conv.ConversationID,
		ActorAgentID:   "ucla.reader",
	}); err == nil {
		t.Fatalf("expected ungranted ucla reader history lookup to be hidden")
	}
}

func TestObserveFilterHidesMixedScopeEventBodiesFromUCLAActor(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.reader", nil, nil)

	now := s.now()
	s.mu.Lock()
	s.publishLocked(
		ObserveMessage,
		map[string]any{"body": "mixed personal observe body"},
		"conv-observe-personal",
		[]string{"ucla.peer", "personal.peer"},
		now,
	)
	s.publishLocked(
		ObserveMessage,
		map[string]any{"body": "mixed shared observe body"},
		"conv-observe-shared",
		[]string{"ucla.peer", "shared.room"},
		now,
	)
	s.publishLocked(
		ObserveMessage,
		map[string]any{"body": "visible ucla observe body"},
		"conv-observe-ucla",
		[]string{"ucla.peer", "ucla.reader"},
		now,
	)
	s.mu.Unlock()

	uclaEvents, _ := s.ObserveSince(0, ObserveFilter{ActorAgentID: "ucla.reader"}, 0)
	foundVisible := false
	for _, evt := range uclaEvents {
		body := fmt.Sprint(evt.Data)
		if strings.Contains(body, "mixed personal observe body") || strings.Contains(body, "mixed shared observe body") {
			t.Fatalf("ucla actor observed mixed-scope event body: %#v", evt)
		}
		if strings.Contains(body, "visible ucla observe body") {
			foundVisible = true
		}
	}
	if !foundVisible {
		t.Fatalf("expected ucla actor to see pure-ucla observe body, got %#v", uclaEvents)
	}

	globalEvents, _ := s.ObserveSince(0, ObserveFilter{}, 0)
	if len(globalEvents) != 4 {
		t.Fatalf("global observe should include registrations plus all mixed events, got %#v", globalEvents)
	}
}

func TestUCLACreatorCannotCreateConversationWithPersonalParticipant(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.creator", nil, nil)

	_, err := s.CreateConversation(CreateConversationInput{
		ConversationID: "conv-create-personal-denied",
		Participants:   []string{"ucla.creator", "personal.peer"},
		ActorAgentID:   "ucla.creator",
	})
	if err == nil {
		t.Fatalf("expected ucla creator to be denied mixed personal participants")
	}
	if !strings.Contains(logs.String(), "identity=ucla.creator") || !strings.Contains(logs.String(), "resource=personal.peer") {
		t.Fatalf("expected create denial log, got %q", logs.String())
	}
}

func TestUCLACreatorCannotReuseExistingPersonalConversationID(t *testing.T) {
	var logs bytes.Buffer
	s := newScopeTestStore(&logs)
	mustRegisterScoped(t, s, "ucla.creator", nil, nil)
	mustRegisterScoped(t, s, "ucla.target", nil, nil)

	conv, err := s.CreateConversation(CreateConversationInput{
		ConversationID: "conv-existing-personal",
		Participants:   []string{"personal.a", "personal.b"},
	})
	if err != nil {
		t.Fatalf("global create personal conversation: %v", err)
	}

	_, err = s.CreateConversation(CreateConversationInput{
		ConversationID: conv.ConversationID,
		Participants:   []string{"ucla.creator", "ucla.target"},
		ActorAgentID:   "ucla.creator",
	})
	if err == nil {
		t.Fatalf("expected ucla creator to be denied existing personal conversation reuse")
	}

	_, _, err = s.SendMessage(SendMessageInput{
		From:           "ucla.creator",
		To:             "ucla.target",
		ConversationID: conv.ConversationID,
		RequestID:      "rid-reuse-personal",
		Type:           MessageTypeRequest,
		Body:           "reuse denied",
	})
	if err == nil {
		t.Fatalf("expected ucla send to be denied existing personal conversation reuse")
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
