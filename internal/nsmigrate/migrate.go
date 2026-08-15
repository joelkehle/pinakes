package nsmigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/joelkehle/pinakes/pkg/bus"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type Options struct {
	DBPath             string
	Authority          Authority
	Apply              bool
	AcknowledgeCopy    bool
	ControlPlaneAgents []string
}

type mapping struct {
	ManifestRow
	active         bool
	alreadyApplied bool
}

type conversationPlan struct {
	id           string
	participants []string
	changed      bool
}

type inspection struct {
	present       map[string]bool
	occurrences   map[string]map[string]int
	conversations []conversationPlan
}

func Run(ctx context.Context, manifest Manifest, options Options) (Report, error) {
	report := newReport(options)
	if !options.AcknowledgeCopy {
		return reject(report, "copy_acknowledgment_required", "database", "refusing to operate without --acknowledge-copy")
	}
	if _, err := ParseAuthority(string(options.Authority)); err != nil {
		return reject(report, "invalid_authority", "authority", err.Error())
	}
	if err := validateDBPath(options.DBPath); err != nil {
		return reject(report, "database_refused", "database", err.Error())
	}

	selected := selectMappings(manifest, options.Authority, &report)
	if len(selected) == 0 {
		return reject(report, "manifest_authority", "manifest", "manifest has no rows for the declared authority")
	}
	if err := validateControlPlaneAgreement(selected, options.ControlPlaneAgents); err != nil {
		return rejectFromError(report, err)
	}
	database, err := openExistingDB(options.DBPath, options.Apply)
	if err != nil {
		return reject(report, "database_refused", "database", err.Error())
	}
	defer database.Close()

	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if options.Apply && isSQLiteBusy(err) {
			return reject(
				report,
				"database_busy",
				"database",
				"another process held a write lock when the apply transaction started",
			)
		}
		return reject(report, "database_error", "database", "begin transaction: "+err.Error())
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	inspected, err := inspect(ctx, tx)
	if err != nil {
		return rejectFromError(report, err)
	}
	if err := preflight(selected, inspected, &report); err != nil {
		return rejectFromError(report, err)
	}
	if report.TotalRewrites == 0 {
		report.Status = "no-op"
		if err := tx.Rollback(); err != nil {
			return reject(report, "database_error", "database", "close read transaction: "+err.Error())
		}
		committed = true
		return report, nil
	}
	if !options.Apply {
		report.Status = "ready"
		if err := tx.Rollback(); err != nil {
			return reject(report, "database_error", "database", "close dry-run transaction: "+err.Error())
		}
		committed = true
		return report, nil
	}

	if err := applyPlan(ctx, tx, selected, inspected); err != nil {
		return reject(report, "apply_failed", "database", err.Error())
	}
	if err := tx.Commit(); err != nil {
		return reject(report, "apply_failed", "database", "commit transaction: "+err.Error())
	}
	committed = true
	report.Status = "applied"
	return report, nil
}

func validateControlPlaneAgreement(mappings []*mapping, configured []string) error {
	manifestIDs := map[string]struct{}{}
	for _, item := range mappings {
		if item.ControlPlane {
			manifestIDs[item.SourceID] = struct{}{}
		}
	}
	normalized, err := bus.NormalizeControlPlaneAgents(configured)
	if err != nil {
		return refuse("control_plane_config", "CONTROL_PLANE_AGENTS", err.Error())
	}
	configuredIDs := map[string]struct{}{}
	for _, agentID := range normalized {
		configuredIDs[agentID] = struct{}{}
	}
	for agentID := range manifestIDs {
		if _, ok := configuredIDs[agentID]; !ok {
			return refuse(
				"control_plane_mismatch",
				"manifest",
				fmt.Sprintf("control-plane identity %q is absent from CONTROL_PLANE_AGENTS", agentID),
			)
		}
	}
	return nil
}

func selectMappings(manifest Manifest, authority Authority, report *Report) []*mapping {
	selected := []*mapping{}
	for _, row := range manifest.Rows {
		if row.SourceAuthority != authority {
			continue
		}
		selected = append(selected, &mapping{ManifestRow: row})
		report.ManifestRows++
		report.ManifestByDisposition[string(row.Disposition)]++
	}
	return selected
}

func validateDBPath(path string) error {
	if path == "" {
		return fmt.Errorf("--db is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("database path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database path must be a regular file")
	}
	return nil
}

func openExistingDB(path string, apply bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	mode := "ro"
	busyTimeout := "5000"
	if apply {
		mode = "rw"
		busyTimeout = "1000"
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() +
		"?mode=" + mode + "&_pragma=busy_timeout(" + busyTimeout + ")&_pragma=foreign_keys(1)"
	if apply {
		dsn += "&_txlock=immediate"
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return database, nil
}

func isSQLiteBusy(err error) bool {
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqlite3.SQLITE_BUSY
}

func inspect(ctx context.Context, tx *sql.Tx) (inspection, error) {
	result := inspection{
		present:     map[string]bool{},
		occurrences: map[string]map[string]int{},
	}
	queries := []struct {
		surface string
		query   string
	}{
		{"agents.agent_id", "SELECT agent_id FROM agents"},
		{"messages.from_agent", "SELECT from_agent FROM messages"},
		{"messages.to_agent", "SELECT to_agent FROM messages WHERE to_agent <> ''"},
		{"deliveries.target_agent_id", "SELECT target_agent_id FROM deliveries"},
		{"delivery_cursors.target_agent_id", "SELECT target_agent_id FROM delivery_cursors"},
		{"idempotency.from_agent", "SELECT from_agent FROM idempotency"},
		{"idempotency.to_agent", "SELECT to_agent FROM idempotency WHERE to_agent <> ''"},
	}
	for _, item := range queries {
		rows, err := tx.QueryContext(ctx, item.query)
		if err != nil {
			return inspection{}, refuse("database_schema", item.surface, "required identity column is unavailable")
		}
		for rows.Next() {
			var identity string
			if err := rows.Scan(&identity); err != nil {
				rows.Close()
				return inspection{}, refuse("database_error", item.surface, "scan identity value: "+err.Error())
			}
			addOccurrence(&result, item.surface, identity)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return inspection{}, refuse("database_error", item.surface, "read identity values: "+err.Error())
		}
		rows.Close()
	}
	if err := inspectConversations(ctx, tx, &result); err != nil {
		return inspection{}, err
	}
	return result, nil
}

func inspectConversations(ctx context.Context, tx *sql.Tx, result *inspection) error {
	rows, err := tx.QueryContext(ctx, "SELECT conversation_id, participants FROM conversations ORDER BY conversation_id")
	if err != nil {
		return refuse("database_schema", "conversations.participants", "required identity column is unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return refuse("database_error", "conversations.participants", "scan participants: "+err.Error())
		}
		var participants []string
		if err := json.Unmarshal([]byte(raw), &participants); err != nil || participants == nil {
			return refuse(
				"invalid_participants_json",
				"conversations.participants",
				"conversation participants must be a JSON array of strings",
			)
		}
		for _, identity := range participants {
			addOccurrence(result, "conversations.participants", identity)
		}
		result.conversations = append(result.conversations, conversationPlan{id: id, participants: participants})
	}
	if err := rows.Err(); err != nil {
		return refuse("database_error", "conversations.participants", "read participants: "+err.Error())
	}
	return nil
}

func addOccurrence(result *inspection, surface, identity string) {
	if identity == "" {
		return
	}
	result.present[identity] = true
	if result.occurrences[surface] == nil {
		result.occurrences[surface] = map[string]int{}
	}
	result.occurrences[surface][identity]++
}

func preflight(mappings []*mapping, inspected inspection, report *Report) error {
	sources := map[string]*mapping{}
	targets := map[string]*mapping{}
	for _, item := range mappings {
		sources[item.SourceID] = item
		targets[item.TargetID] = item
	}
	for identity := range inspected.present {
		if sources[identity] == nil && targets[identity] == nil {
			return refuse(
				"unknown_identity",
				"database",
				"identity is not named by the manifest under the declared authority",
			)
		}
	}
	for _, item := range mappings {
		if item.SourceID == item.TargetID {
			continue
		}
		sourcePresent := inspected.present[item.SourceID]
		targetPresent := inspected.present[item.TargetID]
		if sourcePresent && targetPresent {
			report.Collisions = append(report.Collisions, Collision{
				Code:         "target_occupied",
				SourceID:     item.SourceID,
				TargetID:     item.TargetID,
				ManifestLine: item.Line,
			})
			return refuse(
				"target_occupied",
				fmt.Sprintf("manifest:%d", item.Line),
				"target_id is already occupied while its source identity is still present",
			)
		}
		item.active = sourcePresent
		item.alreadyApplied = !sourcePresent && targetPresent
		if item.active {
			report.PlannedByDisposition[string(item.Disposition)]++
			for _, surface := range surfaces {
				count := inspected.occurrences[surface][item.SourceID]
				report.RewritesBySurface[surface] += count
				report.TotalRewrites += count
			}
		}
		if item.alreadyApplied {
			report.AlreadyAppliedMappings++
		}
	}
	if err := planConversations(mappings, inspected.conversations); err != nil {
		return err
	}
	return nil
}

func planConversations(mappings []*mapping, conversations []conversationPlan) error {
	replacements := map[string]string{}
	for _, item := range mappings {
		if item.active {
			replacements[item.SourceID] = item.TargetID
		}
	}
	for index := range conversations {
		personal, ucla := false, false
		for participantIndex, participant := range conversations[index].participants {
			if target, ok := replacements[participant]; ok {
				conversations[index].participants[participantIndex] = target
				conversations[index].changed = true
				participant = target
			}
			scope, _ := explicitScope(participant)
			personal = personal || scope == "personal"
			ucla = ucla || scope == "ucla"
		}
		if personal && ucla {
			return refuse(
				"mixed_scope_participants",
				"conversations.participants",
				"conversation would contain both personal.* and ucla.* participants",
			)
		}
	}
	return nil
}

func applyPlan(ctx context.Context, tx *sql.Tx, mappings []*mapping, inspected inspection) error {
	updates := []struct {
		surface string
		query   string
	}{
		{"agents.agent_id", "UPDATE agents SET agent_id = ? WHERE agent_id = ?"},
		{"messages.from_agent", "UPDATE messages SET from_agent = ? WHERE from_agent = ?"},
		{"messages.to_agent", "UPDATE messages SET to_agent = ? WHERE to_agent = ? AND to_agent <> ''"},
		{"deliveries.target_agent_id", "UPDATE deliveries SET target_agent_id = ? WHERE target_agent_id = ?"},
		{"delivery_cursors.target_agent_id", "UPDATE delivery_cursors SET target_agent_id = ? WHERE target_agent_id = ?"},
		{"idempotency.from_agent", "UPDATE idempotency SET from_agent = ? WHERE from_agent = ?"},
		{"idempotency.to_agent", "UPDATE idempotency SET to_agent = ? WHERE to_agent = ? AND to_agent <> ''"},
	}
	for _, item := range mappings {
		if !item.active {
			continue
		}
		for _, update := range updates {
			result, err := tx.ExecContext(ctx, update.query, item.TargetID, item.SourceID)
			if err != nil {
				return fmt.Errorf("rewrite %s: %w", update.surface, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count rewritten %s rows: %w", update.surface, err)
			}
			expected := inspected.occurrences[update.surface][item.SourceID]
			if affected != int64(expected) {
				return fmt.Errorf(
					"rewrite %s changed %d rows; preflight planned %d",
					update.surface,
					affected,
					expected,
				)
			}
		}
	}
	conversations := inspected.conversations
	sort.Slice(conversations, func(i, j int) bool { return conversations[i].id < conversations[j].id })
	for _, conversation := range conversations {
		if !conversation.changed {
			continue
		}
		encoded, err := json.Marshal(conversation.participants)
		if err != nil {
			return fmt.Errorf("encode conversations.participants: %w", err)
		}
		result, err := tx.ExecContext(
			ctx,
			"UPDATE conversations SET participants = ? WHERE conversation_id = ?",
			string(encoded),
			conversation.id,
		)
		if err != nil {
			return fmt.Errorf("rewrite conversations.participants: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count rewritten conversations.participants rows: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("rewrite conversations.participants changed %d rows; preflight planned 1", affected)
		}
	}
	return nil
}

func reject(report Report, code, location, detail string) (Report, error) {
	refusal := Refusal{Code: code, Location: location, Detail: detail}
	report.Status = "refused"
	report.Refusals = append(report.Refusals, refusal)
	return report, &RefusalError{Refusals: []Refusal{refusal}}
}

func rejectFromError(report Report, err error) (Report, error) {
	var refusalError *RefusalError
	if !errors.As(err, &refusalError) {
		return reject(report, "database_error", "database", err.Error())
	}
	report.Status = "refused"
	report.Refusals = append(report.Refusals, refusalError.Refusals...)
	return report, err
}
