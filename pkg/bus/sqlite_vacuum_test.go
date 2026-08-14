package bus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewSQLiteStoreUsesIncrementalAutoVacuum proves fresh bus databases are
// created in incremental auto_vacuum mode, so this store's own periodic
// PRAGMA incremental_vacuum (in pruneDB) actually has an effect from day one
// instead of silently no-op'ing the way it would under the pre-fix default
// auto_vacuum=NONE (issue #29).
func TestNewSQLiteStoreUsesIncrementalAutoVacuum(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	s, err := NewSQLiteStore(dbPath, Config{Clock: time.Now})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer s.Close()

	var mode int
	if err := s.db.Get(&mode, "PRAGMA auto_vacuum"); err != nil {
		t.Fatalf("read auto_vacuum: %v", err)
	}
	const autoVacuumIncremental = 2
	if mode != autoVacuumIncremental {
		t.Fatalf("expected auto_vacuum=incremental(2) on a fresh db, got %d", mode)
	}
}

// TestPruneDBReclaimsFreePagesOverChurn proves the SQLiteStore's periodic
// prune sweep does not just delete rows but also shrinks the file over time
// as retention churns through large message bodies — this is the mechanism
// that is supposed to stop issue #29 (1.17 GB file, 99.86% free pages) from
// recurring once a database has gone through pinakes db-compact.
func TestPruneDBReclaimsFreePagesOverChurn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	cfg := Config{
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		MessageRetention:       time.Minute,
		MessageMaxAge:          time.Minute,
		ConversationRetention:  time.Minute,
		Clock:                  clock,
	}

	s, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer s.Close()

	for _, id := range []string{"ucla.a", "ucla.b"} {
		if _, err := s.RegisterAgent(RegisterAgentInput{AgentID: id, Mode: AgentModePull, TTLSeconds: 6000}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	payload := strings.Repeat("y", 4000)
	for i := 0; i < 300; i++ {
		if _, _, err := s.SendMessage(SendMessageInput{
			To: "ucla.b", From: "ucla.a", RequestID: "r" + strconv.Itoa(i), Type: MessageTypeInform, Body: payload,
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	var pageCountLoaded int64
	if err := s.db.Get(&pageCountLoaded, "PRAGMA page_count"); err != nil {
		t.Fatalf("read page_count after load: %v", err)
	}

	// Advance well past retention and run the same sweep the background
	// pruneLoop runs, deleting the terminal messages above.
	mu.Lock()
	current = current.Add(2 * time.Hour)
	mu.Unlock()
	if err := s.pruneDB(clock()); err != nil {
		t.Fatalf("pruneDB: %v", err)
	}

	var msgRows int
	if err := s.db.Get(&msgRows, "SELECT COUNT(*) FROM messages"); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgRows != 0 {
		t.Fatalf("expected all messages pruned, found %d", msgRows)
	}

	var freeAfter, pageCountAfter int64
	if err := s.db.Get(&freeAfter, "PRAGMA freelist_count"); err != nil {
		t.Fatalf("read freelist_count after prune: %v", err)
	}
	if err := s.db.Get(&pageCountAfter, "PRAGMA page_count"); err != nil {
		t.Fatalf("read page_count after prune: %v", err)
	}

	// incremental_vacuum(1000) in pruneDB should have handed every page the
	// 300 deletes freed straight back to the OS well within that cap, so the
	// freelist should be empty (or nearly so) rather than sitting on ~300
	// pages worth of dead message bodies.
	if freeAfter > 5 {
		t.Fatalf("expected freelist to be reclaimed after prune, found %d free pages (loaded db had %d total pages)", freeAfter, pageCountLoaded)
	}
	if pageCountAfter >= pageCountLoaded {
		t.Fatalf("expected page_count to shrink after prune: loaded=%d after=%d", pageCountLoaded, pageCountAfter)
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if info.Size() <= 0 {
		t.Fatalf("expected a non-empty db file")
	}
}
