package nsmigrate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

// SyntheticOptions describes a schema-true, fabricated rehearsal database.
// The destination must not exist; the generator never overwrites a file.
type SyntheticOptions struct {
	DBPath             string
	Authority          Authority
	ControlPlaneAgents []string
}

// SyntheticReport contains only generated fixture metadata.
type SyntheticReport struct {
	Authority     Authority `json:"authority"`
	Identities    int       `json:"identities"`
	Conversations int       `json:"conversations"`
	Messages      int       `json:"messages"`
}

// GenerateSyntheticDB creates a Pinakes SQLite database through the real store
// API using only fabricated content derived from reviewed manifest identities.
func GenerateSyntheticDB(ctx context.Context, manifest Manifest, options SyntheticOptions) (report SyntheticReport, err error) {
	report.Authority = options.Authority
	if _, err := ParseAuthority(string(options.Authority)); err != nil {
		return report, err
	}
	if options.DBPath == "" {
		return report, fmt.Errorf("--db is required")
	}
	if _, err := os.Lstat(options.DBPath); err == nil {
		return report, fmt.Errorf("synthetic database destination already exists")
	} else if !os.IsNotExist(err) {
		return report, fmt.Errorf("stat synthetic database destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(options.DBPath), 0o700); err != nil {
		return report, fmt.Errorf("create synthetic database directory: %w", err)
	}

	selected := selectMappings(manifest, options.Authority, &Report{
		ManifestByDisposition: map[string]int{},
	})
	if len(selected) == 0 {
		return report, fmt.Errorf("manifest has no rows for authority %s", options.Authority)
	}
	if err := validateControlPlaneAgreement(selected, options.ControlPlaneAgents); err != nil {
		return report, err
	}

	sharedAgents := []string{}
	groups := map[string][]string{}
	for _, item := range selected {
		identity := item.SourceID
		scope, ok := explicitScope(identity)
		if !ok {
			scope = string(options.Authority)
			if options.Authority == AuthorityJK {
				scope = "personal"
			}
		}
		groups[scope] = append(groups[scope], identity)
		if scope == "shared" {
			sharedAgents = append(sharedAgents, identity)
		}
	}
	for scope := range groups {
		sort.Strings(groups[scope])
	}

	clock := func() time.Time {
		return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	}
	controlPlaneHashes := map[string][sha256.Size]byte{}
	for _, agentID := range options.ControlPlaneAgents {
		controlPlaneHashes[agentID] = sha256.Sum256([]byte(syntheticRegistrationSecret(agentID)))
	}
	store, err := bus.NewSQLiteStore(options.DBPath, bus.Config{
		Clock:                         clock,
		NamespaceMode:                 bus.NamespaceModeCompat,
		LegacyScope:                   legacyScope(options.Authority),
		SharedGrantAgents:             sharedAgents,
		ControlPlaneAgents:            options.ControlPlaneAgents,
		ControlPlaneAgentSecretHashes: controlPlaneHashes,
		MessageRetention:              -1,
		MessageMaxAge:                 -1,
		ConversationRetention:         -1,
		AgentRetention:                -1,
	})
	if err != nil {
		return report, fmt.Errorf("create synthetic sqlite store: %w", err)
	}
	complete := false
	defer func() {
		closeErr := store.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("close synthetic sqlite store: %w", closeErr)
		}
		if !complete {
			_ = os.Remove(options.DBPath)
			_ = os.Remove(options.DBPath + "-shm")
			_ = os.Remove(options.DBPath + "-wal")
		}
	}()

	identities := make([]string, 0, len(selected))
	for _, item := range selected {
		identities = append(identities, item.SourceID)
	}
	sort.Strings(identities)
	for index, identity := range identities {
		if err := context.Cause(ctx); err != nil {
			return report, err
		}
		if _, err := store.RegisterAgent(bus.RegisterAgentInput{
			AgentID:      identity,
			Secret:       syntheticRegistrationSecret(identity),
			Capabilities: []string{"synthetic-rehearsal"},
			Description:  "fabricated namespace migration fixture",
			AgentClass:   "worker",
			Mode:         bus.AgentModePull,
			TTLSeconds:   3600,
			Meta: &bus.AgentMeta{
				Owner: "synthetic-only",
				Repo:  "github.com/joelkehle/pinakes",
			},
		}); err != nil {
			return report, fmt.Errorf("register synthetic identity %q: %w", identity, err)
		}
		if err := store.SetAgentSecret(identity, fmt.Sprintf("synthetic-secret-%03d", index+1)); err != nil {
			return report, fmt.Errorf("set synthetic secret for %q: %w", identity, err)
		}
	}
	report.Identities = len(identities)

	groupNames := make([]string, 0, len(groups))
	for scope := range groups {
		groupNames = append(groupNames, scope)
	}
	sort.Strings(groupNames)
	sequence := 0
	for _, scope := range groupNames {
		members := groups[scope]
		for index, sender := range members {
			if err := context.Cause(ctx); err != nil {
				return report, err
			}
			target := members[(index+1)%len(members)]
			sequence++
			conversationID := fmt.Sprintf("synthetic-%s-%03d", scope, sequence)
			participants := []string{sender}
			if target != sender {
				participants = append(participants, target)
			}
			if _, err := store.CreateConversation(bus.CreateConversationInput{
				ConversationID: conversationID,
				Title:          "fabricated migration rehearsal",
				Participants:   participants,
				Meta: map[string]any{
					"synthetic":               true,
					"protected_identity_text": sender,
				},
			}); err != nil {
				return report, fmt.Errorf("create synthetic conversation %q: %w", conversationID, err)
			}
			message, _, err := store.SendMessage(bus.SendMessageInput{
				From:           sender,
				To:             target,
				ConversationID: conversationID,
				RequestID:      fmt.Sprintf("synthetic-request-%03d", sequence),
				Type:           bus.MessageTypeInform,
				Body:           "fabricated content; identity text must not be rewritten: " + sender,
				Meta: map[string]any{
					"synthetic":               true,
					"protected_identity_text": target,
				},
			})
			if err != nil {
				return report, fmt.Errorf("send synthetic message %d: %w", sequence, err)
			}
			events, cursor, err := store.PollInbox(bus.PollInboxInput{AgentID: target})
			if err != nil {
				return report, fmt.Errorf("poll synthetic inbox %q: %w", target, err)
			}
			if len(events) == 0 || events[len(events)-1].MessageID != message.MessageID {
				return report, fmt.Errorf("synthetic delivery %q was not observable", message.MessageID)
			}
			if _, _, err := store.PollInbox(bus.PollInboxInput{AgentID: target, Cursor: cursor}); err != nil {
				return report, fmt.Errorf("advance synthetic cursor %q: %w", target, err)
			}
			report.Conversations++
			report.Messages++
		}
	}

	complete = true
	return report, nil
}

func syntheticRegistrationSecret(agentID string) string {
	return "fabricated-registration-secret:" + agentID
}

func legacyScope(authority Authority) bus.Scope {
	if authority == AuthorityJK {
		return bus.ScopePersonal
	}
	return bus.ScopeUCLA
}
