package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/joelkehle/pinakes/internal/configutil"
	"github.com/joelkehle/pinakes/pkg/bus"
	"github.com/joelkehle/pinakes/pkg/httpapi"
)

type rehearsalReport struct {
	Status                     string   `json:"status"`
	NamespaceMode              string   `json:"namespace_mode"`
	EmptyBeforeRegistration    bool     `json:"empty_before_registration"`
	RegisteredAgents           []string `json:"registered_agents"`
	UnlistedUnprefixedRejected bool     `json:"unlisted_unprefixed_rejected"`
	DistinctSyntheticSecrets   bool     `json:"distinct_synthetic_secrets"`
	ControlPlaneAllScopes      bool     `json:"control_plane_all_scopes"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("pinakes-empty-strict-rehearsal", flag.ContinueOnError)
	dbPath := flags.String("db", "", "new empty SQLite database path")
	allowlistPath := flags.String("allowlist", "", "rehearsal allowlist path")
	agentsRaw := flags.String("agents", "", "comma-separated representative namespaced agents")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *dbPath == "" || *allowlistPath == "" || *agentsRaw == "" {
		fmt.Fprintln(os.Stderr, "usage: pinakes-empty-strict-rehearsal --db NEW.db --allowlist allowlist.txt --agents id,id,...")
		return 2
	}
	if _, err := os.Lstat(*dbPath); err == nil {
		fmt.Fprintln(os.Stderr, "empty-store destination already exists")
		return 1
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "stat empty-store destination: %v\n", err)
		return 1
	}
	controlPlaneAgents := configutil.SplitCSV(os.Getenv("CONTROL_PLANE_AGENTS"))
	if len(controlPlaneAgents) == 0 {
		fmt.Fprintln(os.Stderr, "CONTROL_PLANE_AGENTS must name the reviewed rehearsal control plane")
		return 1
	}
	agents := configutil.SplitCSV(*agentsRaw)
	agents = append(agents, controlPlaneAgents...)

	store, err := bus.NewSQLiteStore(*dbPath, bus.Config{
		NamespaceMode:      bus.NamespaceModeStrict,
		ControlPlaneAgents: controlPlaneAgents,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create empty strict-mode store: %v\n", err)
		return 1
	}
	defer store.Close()
	report := rehearsalReport{
		Status:                  "refused",
		NamespaceMode:           "strict",
		EmptyBeforeRegistration: len(store.ListAgents("")) == 0,
	}

	previousAllowlist, hadAllowlist := os.LookupEnv("ALLOWLIST_FILE")
	if err := os.Setenv("ALLOWLIST_FILE", *allowlistPath); err != nil {
		fmt.Fprintf(os.Stderr, "set rehearsal allowlist: %v\n", err)
		return 1
	}
	defer func() {
		if hadAllowlist {
			_ = os.Setenv("ALLOWLIST_FILE", previousAllowlist)
		} else {
			_ = os.Unsetenv("ALLOWLIST_FILE")
		}
	}()
	handler, err := httpapi.NewServerFromEnv(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start rehearsal handler: %v\n", err)
		return 1
	}

	seen := map[string]struct{}{}
	for index, agentID := range agents {
		if _, ok := seen[agentID]; ok {
			continue
		}
		seen[agentID] = struct{}{}
		secret := fmt.Sprintf("synthetic-registration-secret-%03d", index+1)
		status := register(handler, agentID, secret)
		if status != http.StatusOK {
			fmt.Fprintf(os.Stderr, "register %s: HTTP %d\n", agentID, status)
			return 1
		}
		report.RegisteredAgents = append(report.RegisteredAgents, agentID)
	}
	_, strictErr := store.RegisterAgent(bus.RegisterAgentInput{
		AgentID: "synthetic-unlisted",
		Mode:    bus.AgentModePull,
	})
	report.UnlistedUnprefixedRejected = strictErr != nil

	secrets, err := store.AgentSecrets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "read synthetic secrets: %v\n", err)
		return 1
	}
	uniqueSecrets := map[string]struct{}{}
	for _, agentID := range report.RegisteredAgents {
		uniqueSecrets[secrets[agentID]] = struct{}{}
	}
	report.DistinctSyntheticSecrets = len(uniqueSecrets) == len(report.RegisteredAgents)

	for _, agent := range store.ListAgents("") {
		for _, controlPlaneID := range controlPlaneAgents {
			if agent.AgentID == controlPlaneID && strings.Join(agent.AllowedScopes, ",") == "personal,ucla,shared" {
				report.ControlPlaneAllScopes = true
			}
		}
	}
	if !report.EmptyBeforeRegistration || !report.UnlistedUnprefixedRejected ||
		!report.DistinctSyntheticSecrets || !report.ControlPlaneAllScopes {
		encoded, _ := json.Marshal(report)
		fmt.Fprintf(os.Stderr, "strict-mode rehearsal checks failed: %s\n", encoded)
		return 1
	}
	report.Status = "passed"
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		return 1
	}
	return 0
}

func register(handler http.Handler, agentID, secret string) int {
	body, _ := json.Marshal(map[string]any{
		"agent_id": agentID,
		"mode":     "pull",
		"ttl":      60,
		"secret":   secret,
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}
