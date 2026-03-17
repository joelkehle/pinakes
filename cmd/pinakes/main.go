package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
	"github.com/joelkehle/pinakes/pkg/httpapi"
)

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

	h := httpapi.NewServer(store)
	log.Printf("techtransfer-agency listening on %s", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		log.Fatal(err)
	}
}
