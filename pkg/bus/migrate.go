package bus

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// migrationCounts tallies imported rows for the one-line success log.
type migrationCounts struct {
	agents        int
	secrets       int
	conversations int
	messages      int
	links         int
	deliveries    int
	idempotency   int
}

// MigrateJSONStateToSQLite performs a one-time import of the legacy JSON
// state file into a fresh SQLite database. It is a no-op (returns false, nil)
// when the SQLite DB file already exists or the state file does not exist.
// On success the state file is renamed to <statePath>.migrated and kept as a
// backup. The import writes to <dbPath>.tmp and atomically renames it into
// place only after the import transaction commits, so a crash mid-import leaves
// only temp artifacts that the next boot removes before retrying. On any import
// error the temp DB file is removed so the migration retries on the next boot;
// callers must treat the error as fatal rather than booting an empty bus.
//
// Durable entities are imported: agents (registration metadata), agent
// secrets, conversations, messages (including terminal/lifecycle timestamps),
// conversation message positions, direct-delivery buffers, pull cursors,
// duplicate receipts, and id counters. Observer events are intentionally
// dropped because they are process-local.
func MigrateJSONStateToSQLite(statePath, dbPath string, cfg Config) (bool, error) {
	if statePath == "" || dbPath == "" {
		return false, nil
	}
	tmpPath := dbPath + ".tmp"
	if _, err := os.Stat(dbPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat sqlite db %s: %w", dbPath, err)
	}
	blob, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read legacy state %s: %w", statePath, err)
	}
	// Crashes mid-import can leave temp DB and WAL artifacts behind; remove
	// them before retrying into a fresh temp path.
	_ = os.Remove(tmpPath)
	_ = os.Remove(tmpPath + "-wal")
	_ = os.Remove(tmpPath + "-shm")

	var state persistentState
	if err := json.Unmarshal(blob, &state); err != nil {
		return false, fmt.Errorf("parse legacy state %s: %w", statePath, err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return false, fmt.Errorf("create sqlite db dir: %w", err)
	}
	counts, err := importStateToSQLite(state, tmpPath, cfg)
	if err != nil {
		// Remove the failed temp import (plus WAL siblings, best effort) and let
		// the next boot retry instead of skipping migration forever.
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
		return false, fmt.Errorf("import legacy state %s into sqlite %s: %w", statePath, dbPath, err)
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
		return false, fmt.Errorf("rename migrated sqlite db %s to %s: %w", tmpPath, dbPath, err)
	}
	if err := os.Rename(statePath, statePath+".migrated"); err != nil {
		return false, fmt.Errorf("rename migrated state file %s: %w", statePath, err)
	}
	log.Printf("INFO migrated JSON state %s -> sqlite %s: agents=%d secrets=%d conversations=%d messages=%d conversation_links=%d deliveries=%d idempotency=%d",
		statePath, dbPath, counts.agents, counts.secrets, counts.conversations,
		counts.messages, counts.links, counts.deliveries, counts.idempotency)
	return true, nil
}

// importStateToSQLite writes the durable entities from a decoded legacy state
// into a new SQLite database in one transaction (all-or-nothing).
func importStateToSQLite(state persistentState, dbPath string, cfg Config) (migrationCounts, error) {
	var counts migrationCounts

	// Same pragma ordering as NewSQLiteStore in sqlite.go, and for the same
	// reason (issue #29): journal_mode=WAL is deliberately not set via the
	// connection-string _pragma option, because that applies at connect
	// time, before we get a chance to run our own PRAGMA auto_vacuum below.
	// Switching into WAL mode writes the database's page 1 immediately, and
	// auto_vacuum only takes effect without a VACUUM while the database is
	// still completely empty — so it must run first, against a tmpPath file
	// that has never been written to.
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return counts, fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return counts, fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return counts, fmt.Errorf("set journal_mode: %w", err)
	}

	if _, err := db.Exec(sqliteSchema); err != nil {
		return counts, fmt.Errorf("create schema: %w", err)
	}

	tx, err := db.Beginx()
	if err != nil {
		return counts, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, a := range state.Agents {
		cp := a
		if err := saveAgentTo(tx, &cp); err != nil {
			return counts, fmt.Errorf("import agent %s: %w", cp.AgentID, err)
		}
		counts.agents++
	}
	// saveAgentTo never writes the secret column (registration must not be
	// able to overwrite an established secret), so secrets are applied as a
	// second pass. Secrets without a matching agent row are dropped.
	for agentID, secret := range state.AgentSecrets {
		res, err := tx.Exec("UPDATE agents SET secret = ? WHERE agent_id = ?", secret, agentID)
		if err != nil {
			return counts, fmt.Errorf("import secret for %s: %w", agentID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			counts.secrets++
		} else if err == nil && n == 0 {
			// Orphan secret: no agent row matched, so the secret is unusable and
			// dropped. Warn so a lost credential is visible in the migration log.
			log.Printf("WARN dropping secret for unknown agent %s during migration", agentID)
		}
	}
	for _, c := range state.Conversations {
		cp := c
		if err := saveConversationTo(tx, &cp); err != nil {
			return counts, fmt.Errorf("import conversation %s: %w", cp.ConversationID, err)
		}
		counts.conversations++
	}
	for _, pm := range state.Messages {
		m := pm.toMessage()
		if err := saveMessageTo(tx, &m); err != nil {
			return counts, fmt.Errorf("import message %s: %w", m.MessageID, err)
		}
		counts.messages++
	}
	for cid, mids := range state.ConversationMessages {
		for position, mid := range mids {
			if err := saveConversationMessageTo(tx, cid, mid, position); err != nil {
				return counts, fmt.Errorf("import conversation link %s/%s: %w", cid, mid, err)
			}
			counts.links++
		}
	}
	if err := saveCountersTo(tx, state.NextConversationID, state.NextMessageID); err != nil {
		return counts, fmt.Errorf("import counters: %w", err)
	}
	defaultTTL := cfg.DefaultMessageTTL
	if defaultTTL <= 0 {
		defaultTTL = 10 * time.Minute
	}
	for agentID, inbox := range state.Inboxes {
		base := state.InboxBase[agentID]
		deliverable := make([]InboxEvent, 0, len(inbox))
		for _, event := range inbox {
			pm, ok := state.Messages[event.MessageID]
			if !ok {
				log.Printf("WARN dropping delivery for unknown message %s during migration", event.MessageID)
				continue
			}
			message := pm.toMessage()
			if message.Type == MessageTypeRequest &&
				(message.State == StateExecuting || isTerminal(message.State)) {
				continue
			}
			deliverable = append(deliverable, event)
		}
		if _, err := tx.Exec(`INSERT INTO delivery_cursors
			(target_agent_id, next_seq, acknowledged_cursor) VALUES (?, ?, ?)`,
			agentID, base+len(deliverable), base); err != nil {
			return counts, fmt.Errorf("import delivery cursor %s: %w", agentID, err)
		}
		for offset, event := range deliverable {
			pm, ok := state.Messages[event.MessageID]
			if !ok {
				continue
			}
			message := pm.toMessage()
			expiresAt := message.TTLExpiresAt
			if expiresAt.IsZero() {
				expiresAt = message.CreatedAt.Add(defaultTTL)
			}
			if !message.GraceUntil.IsZero() && message.GraceUntil.Before(expiresAt) {
				expiresAt = message.GraceUntil
			}
			if _, err := tx.Exec(`INSERT INTO deliveries
				(target_agent_id, delivery_seq, message_id, status, next_attempt_at,
				 attempt_count, received_at, expires_at, last_error)
				VALUES (?, ?, ?, 'pending', ?, 0, '', ?, '')`,
				agentID, base+offset, event.MessageID,
				timeToString(message.CreatedAt), timeToString(expiresAt)); err != nil {
				return counts, fmt.Errorf("import delivery %s/%s: %w", agentID, event.MessageID, err)
			}
			counts.deliveries++
		}
	}
	for agentID, base := range state.InboxBase {
		if _, ok := state.Inboxes[agentID]; ok {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO delivery_cursors
			(target_agent_id, next_seq, acknowledged_cursor) VALUES (?, ?, ?)`,
			agentID, base, base); err != nil {
			return counts, fmt.Errorf("import empty delivery cursor %s: %w", agentID, err)
		}
	}
	idempotencyWindow := cfg.IdempotencyWindow
	if idempotencyWindow <= 0 {
		idempotencyWindow = 24 * time.Hour
	}
	for key, entry := range state.Idempotency {
		parts := strings.Split(key, "\x1f")
		if len(parts) != 3 {
			log.Printf("WARN dropping malformed idempotency key during migration")
			continue
		}
		if _, err := tx.Exec(`INSERT INTO idempotency
			(from_agent, to_agent, request_id, message_id, accepted_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			parts[0], parts[1], parts[2], entry.MessageID,
			timeToString(entry.CreatedAt), timeToString(entry.CreatedAt.Add(idempotencyWindow))); err != nil {
			return counts, fmt.Errorf("import idempotency receipt for %s: %w", entry.MessageID, err)
		}
		counts.idempotency++
	}
	appliedAt := time.Now().UTC()
	if cfg.Clock != nil {
		appliedAt = cfg.Clock().UTC()
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (key, applied_at)
		VALUES (?, ?)`, durableDeliveryBackfill, timeToString(appliedAt)); err != nil {
		return counts, fmt.Errorf("mark durable delivery migration complete: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return counts, err
	}
	committed = true
	return counts, nil
}
