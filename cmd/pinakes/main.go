package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
	"github.com/joelkehle/pinakes/pkg/httpapi"
)

// envSeconds parses an integer-seconds env var into a duration. Unset or
// invalid returns 0 (use the store default); negative values disable the knob.
func envSeconds(name string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN ignoring invalid %s=%q", name, raw)
		return 0
	}
	if v < 0 {
		return -1
	}
	return time.Duration(v) * time.Second
}

func envInt(name string) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN ignoring invalid %s=%q", name, raw)
		return 0
	}
	if v < 0 {
		return -1
	}
	return v
}

func envCSV(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, entry := range strings.Split(raw, ",") {
		value := strings.TrimSpace(entry)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func parseNamespaceConfig(modeRaw, legacyScopeRaw string) (bus.NamespaceMode, bus.Scope, error) {
	mode := bus.NamespaceMode(strings.TrimSpace(modeRaw))
	if mode == "" {
		mode = bus.NamespaceModeCompat
	}
	if mode != bus.NamespaceModeCompat && mode != bus.NamespaceModeStrict {
		return "", "", fmt.Errorf("BUS_NAMESPACE_MODE must be compat or strict, got %q", modeRaw)
	}

	legacyScope := bus.Scope(strings.TrimSpace(legacyScopeRaw))
	if legacyScope == "" {
		legacyScope = bus.ScopeUCLA
	}
	if legacyScope != bus.ScopePersonal && legacyScope != bus.ScopeUCLA {
		return "", "", fmt.Errorf("BUS_LEGACY_SCOPE must be personal or ucla, got %q", legacyScopeRaw)
	}

	return mode, legacyScope, nil
}

func runHTTPServer(addr string, handler http.Handler, store bus.API) {
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout/WriteTimeout stay unset: inbox long-polls can run for
		// 60s, and observe SSE streams are intentionally open-ended.
	}

	log.Printf("pinakes listening on %s", addr)
	// Buffered so the goroutine can deliver a listen error and exit even if the
	// main goroutine already left the select (e.g. on a concurrent signal).
	srvErr := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()

	// Both exit paths converge here so the store is always closed through one
	// code path. A listen failure (e.g. port already in use) used to log.Fatal
	// inside the goroutine, skipping the store close below.
	var listenErr error
	select {
	case <-shutdownCtx.Done():
		log.Printf("shutdown requested")
	case listenErr = <-srvErr:
		log.Printf("http listen error: %v", listenErr)
	}
	stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		// Long-poll/SSE clients retry; exit after the grace window even if
		// in-flight requests keep the server from draining cleanly.
		log.Printf("WARN http shutdown: %v", err)
	}

	if c, ok := store.(io.Closer); ok {
		if err := c.Close(); err != nil {
			log.Printf("WARN closing store: %v", err)
		}
	}

	// Exit non-zero only after the store is closed, so a port conflict still
	// surfaces as a failure without skipping cleanup.
	if listenErr != nil {
		os.Exit(1)
	}
}

func main() {
	dbFlag := flag.String("db", "", "path to SQLite database file (overrides DB_PATH env var)")
	flag.Parse()

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	namespaceMode, legacyScope, err := parseNamespaceConfig(
		os.Getenv("BUS_NAMESPACE_MODE"),
		os.Getenv("BUS_LEGACY_SCOPE"),
	)
	if err != nil {
		log.Fatalf("invalid namespace configuration: %v", err)
	}

	cfg := bus.Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           60 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		PushMaxAttempts:        3,
		PushBaseBackoff:        500 * time.Millisecond,
		MaxInboxEventsPerAgent: 10000,
		MaxObserveEvents:       50000,
		// Retention/byte-budget knobs default inside bus.NewStore; envs
		// override. Set a *_SECONDS env to -1 to disable that knob.
		MessageRetention:      envSeconds("MESSAGE_RETENTION_SECONDS"),
		MessageMaxAge:         envSeconds("MESSAGE_MAX_AGE_SECONDS"),
		ConversationRetention: envSeconds("CONVERSATION_RETENTION_SECONDS"),
		AgentRetention:        envSeconds("AGENT_RETENTION_SECONDS"),
		MaxInboxBytesPerAgent: envInt("MAX_INBOX_BYTES_PER_AGENT"),
		MaxObserveBytes:       envInt("MAX_OBSERVE_BYTES"),
		NamespaceMode:         namespaceMode,
		LegacyScope:           legacyScope,
		SharedGrantAgents:     envCSV("SHARED_GRANT_AGENTS"),
	}

	// Resolve DB path: --db flag > DB_PATH env > backend default. An explicit
	// path always selects SQLite; otherwise STORE_BACKEND picks the backend
	// and defaults to sqlite when unset.
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("DB_PATH")
	}
	backend := strings.TrimSpace(os.Getenv("STORE_BACKEND"))
	if dbPath != "" || backend == "" {
		backend = "sqlite"
	}

	statePath := os.Getenv("STATE_FILE")
	if statePath == "" {
		statePath = "./data/state.json"
	}

	var store bus.API
	switch backend {
	case "sqlite":
		if dbPath == "" {
			dbPath = "./data/bus.db"
		}
		// One-time import of the legacy JSON state file: runs only when the
		// DB file does not exist yet and the state file does. Failing loudly
		// here beats silently booting an empty bus over live state.
		if _, err := bus.MigrateJSONStateToSQLite(statePath, dbPath, cfg); err != nil {
			log.Fatalf("failed to migrate legacy JSON state into sqlite: %v", err)
		}
		ss, err := bus.NewSQLiteStore(dbPath, cfg)
		if err != nil {
			log.Fatalf("failed to initialize sqlite store (%s): %v", dbPath, err)
		}
		store = ss
		log.Printf("using sqlite store at %s", dbPath)
	case "memory":
		store = bus.NewStore(cfg)
	default:
		// "persistent", "json", and (for backward compatibility) any other
		// unrecognized value select the legacy JSON-file backend.
		if backend != "persistent" && backend != "json" {
			log.Printf("WARN unrecognized STORE_BACKEND=%q, using persistent JSON backend", backend)
		}
		ps, err := bus.NewPersistentStore(statePath, cfg)
		if err != nil {
			log.Fatalf("failed to initialize persistent store (%s): %v", statePath, err)
		}
		store = ps
		log.Printf("using persistent store at %s", statePath)
	}

	server, err := httpapi.NewServerFromEnv(store)
	if err != nil {
		log.Fatalf("failed to initialize http server: %v", err)
	}
	if allowlistFile := strings.TrimSpace(os.Getenv("ALLOWLIST_FILE")); allowlistFile != "" {
		if err := server.WatchAllowlistFile(context.Background(), allowlistFile); err != nil {
			log.Fatalf("failed to watch allowlist file %s: %v", allowlistFile, err)
		}
		log.Printf("watching allowlist file %s", allowlistFile)
	}
	runHTTPServer(addr, server, store)
}
