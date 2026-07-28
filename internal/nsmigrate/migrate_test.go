package nsmigrate

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"testing"
)

func TestGoldenDryRun(t *testing.T) {
	path := createFixtureDB(t)
	manifest := loadManifestFixture(t, "success_manifest.csv")
	before := snapshotDB(t, path)

	report, err := Run(context.Background(), manifest, Options{
		DBPath:          path,
		Authority:       AuthorityJK,
		AcknowledgeCopy: true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	golden, err := os.ReadFile(fixturePath("golden_dry_run.json"))
	if err != nil {
		t.Fatalf("read golden report: %v", err)
	}
	if got := formattedReport(t, report); !slices.Equal(got, golden) {
		t.Fatalf("dry-run report mismatch\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("dry-run changed the database")
	}
}

func TestApplyIdempotenceAndInverse(t *testing.T) {
	path := createFixtureDB(t)
	manifest := loadManifestFixture(t, "success_manifest.csv")
	original := snapshotDB(t, path)

	report, err := runApply(t, path, manifest)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Status != "applied" || report.TotalRewrites != 14 {
		t.Fatalf("apply report = %#v", report)
	}
	verifyAppliedFixture(t, path)

	second, err := runApply(t, path, manifest)
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if second.Status != "no-op" || second.TotalRewrites != 0 || second.AlreadyAppliedMappings != 3 {
		t.Fatalf("second apply report = %#v", second)
	}

	inverse := inverseManifest(t, manifest)
	undo, err := runApply(t, path, inverse)
	if err != nil {
		t.Fatalf("inverse apply: %v", err)
	}
	if undo.Status != "applied" || undo.TotalRewrites != 14 {
		t.Fatalf("inverse report = %#v", undo)
	}
	if restored := snapshotDB(t, path); restored != original {
		t.Fatal("inverse manifest did not restore the original logical database")
	}
}

func TestApplyFailureRollsBackEverySurface(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `CREATE TRIGGER refuse_message_rewrite
		BEFORE UPDATE OF from_agent ON messages
		BEGIN
			SELECT RAISE(ABORT, 'synthetic apply failure');
		END`)
	before := snapshotDB(t, path)

	report, err := runApply(t, path, loadManifestFixture(t, "success_manifest.csv"))
	if err == nil {
		t.Fatalf("expected apply failure, report %#v", report)
	}
	if code := refusalCode(t, err); code != "apply_failed" {
		t.Fatalf("refusal code = %q, want apply_failed", code)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("failed apply did not roll back every surface")
	}
}

func verifyAppliedFixture(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatalf("open applied fixture: %v", err)
	}
	defer database.Close()

	assertStrings(t, database,
		"SELECT agent_id FROM agents ORDER BY agent_id",
		[]string{
			"personal.archive-daemon",
			"personal.atlas-ingest",
			"personal.dual-canary",
			"personal.steady-worker",
		},
	)
	assertStrings(t, database,
		"SELECT participants FROM conversations",
		[]string{`["personal.atlas-ingest","personal.archive-daemon","personal.dual-canary","personal.steady-worker"]`},
	)
	assertStrings(t, database,
		"SELECT from_agent || '|' || to_agent FROM messages ORDER BY message_id",
		[]string{"personal.atlas-ingest|personal.archive-daemon", "personal.dual-canary|"},
	)
	assertStrings(t, database,
		"SELECT target_agent_id FROM deliveries",
		[]string{"personal.archive-daemon"},
	)
	assertStrings(t, database,
		"SELECT target_agent_id FROM delivery_cursors",
		[]string{"personal.archive-daemon"},
	)
	assertStrings(t, database,
		"SELECT from_agent || '|' || to_agent FROM idempotency ORDER BY request_id",
		[]string{"personal.atlas-ingest|personal.archive-daemon", "personal.dual-canary|"},
	)

	// Protected fields deliberately contain legacy IDs and must remain byte-for-byte.
	assertStrings(t, database,
		"SELECT secret FROM agents ORDER BY agent_id",
		[]string{"secret-archive", "secret-atlas", "secret-dual", "secret-steady"},
	)
	assertStrings(t, database,
		"SELECT callback_url FROM agents ORDER BY agent_id",
		[]string{
			"https://invalid/archive-daemon",
			"https://invalid/atlas-ingest",
			"https://invalid/dual-canary",
			"https://invalid/personal.steady-worker",
		},
	)
	assertStrings(t, database,
		"SELECT title FROM conversations",
		[]string{"atlas-ingest archive-daemon dual-canary title must remain"},
	)
	assertStrings(t, database,
		"SELECT body FROM messages ORDER BY message_id",
		[]string{
			"atlas-ingest and archive-daemon body must remain",
			"dual-canary broadcast body must remain",
		},
	)
	assertStrings(t, database,
		"SELECT last_error FROM deliveries",
		[]string{"archive-daemon last_error must remain"},
	)
	assertStrings(t, database,
		"SELECT meta FROM messages ORDER BY message_id",
		[]string{`{"identity":"atlas-ingest"}`, `{"identity":"dual-canary"}`},
	)
	assertStrings(t, database,
		"SELECT attachments FROM messages ORDER BY message_id",
		[]string{`[{"name":"archive-daemon"}]`, `[]`},
	)
}

func assertStrings(t *testing.T, database *sql.DB, query string, want []string) {
	t.Helper()
	rows, err := database.Query(query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		got = append(got, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %q: %v", query, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("query %q = %#v, want %#v", query, got, want)
	}
}
