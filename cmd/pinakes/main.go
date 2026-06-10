package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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

func main() {
	dbFlag := flag.String("db", "", "path to SQLite database file (overrides DB_PATH env var)")
	flag.Parse()

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
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
	}

	// Resolve DB path: --db flag > DB_PATH env > empty (use legacy backend).
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("DB_PATH")
	}

	var store bus.API
	if dbPath != "" {
		ss, err := bus.NewSQLiteStore(dbPath, cfg)
		if err != nil {
			log.Fatalf("failed to initialize sqlite store (%s): %v", dbPath, err)
		}
		store = ss
		log.Printf("using sqlite store at %s", dbPath)
	} else {
		backend := os.Getenv("STORE_BACKEND")
		if backend == "" {
			backend = "persistent"
		}
		switch backend {
		case "memory":
			store = bus.NewStore(cfg)
		default:
			statePath := os.Getenv("STATE_FILE")
			if statePath == "" {
				statePath = "./data/state.json"
			}
			ps, err := bus.NewPersistentStore(statePath, cfg)
			if err != nil {
				log.Fatalf("failed to initialize persistent store (%s): %v", statePath, err)
			}
			store = ps
			log.Printf("using persistent store at %s", statePath)
		}
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
	log.Printf("pinakes listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}
