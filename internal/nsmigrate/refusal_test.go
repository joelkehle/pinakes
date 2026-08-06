package nsmigrate

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestFailClosedUnknownIdentityBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path,
		"INSERT INTO messages VALUES ('msg-unknown', 'unknown-worker', '', 'private body', '{}', '[]')")
	assertApplyRefusedWithoutWrite(t, path, loadManifestFixture(t, "success_manifest.csv"), "unknown_identity")
}

func TestFailClosedDuplicateTargetBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	before := snapshotDB(t, path)
	file, err := os.Open(fixturePath("fail_duplicate_target.csv"))
	if err != nil {
		t.Fatalf("open duplicate fixture: %v", err)
	}
	_, err = ParseManifest(file)
	file.Close()
	if code := refusalCode(t, err); code != "duplicate_target" {
		t.Fatalf("refusal code = %q, want duplicate_target", code)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("manifest refusal changed the database")
	}
}

func TestFailClosedOccupiedTargetBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('personal.atlas-ingest', 'other-secret', 'https://invalid/occupied', 'occupied', '{}')`)
	before := snapshotDB(t, path)
	report, err := runApply(t, path, loadManifestFixture(t, "success_manifest.csv"))
	if code := refusalCode(t, err); code != "target_occupied" {
		t.Fatalf("refusal code = %q, want target_occupied", code)
	}
	if len(report.Collisions) != 1 ||
		report.Collisions[0].SourceID != "atlas-ingest" ||
		report.Collisions[0].TargetID != "personal.atlas-ingest" {
		t.Fatalf("collision report = %#v", report.Collisions)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("target collision changed the database")
	}
}

func TestFailClosedMixedScopeParticipantsBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('ucla.foreign-worker', 'foreign-secret', 'https://invalid/foreign', 'foreign', '{}')`)
	execFixtureSQL(t, path,
		`UPDATE conversations SET participants = '["atlas-ingest","ucla.foreign-worker"]'`)

	base, err := os.ReadFile(fixturePath("success_manifest.csv"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest, err := ParseManifest(strings.NewReader(string(base) +
		"jk,ucla.foreign-worker,ucla.foreign-worker,unchanged,example/repo,synthetic foreign scope\n"))
	if err != nil {
		t.Fatalf("parse mixed-scope manifest: %v", err)
	}
	assertApplyRefusedWithoutWrite(t, path, manifest, "mixed_scope_participants")
}

func TestFailClosedInvalidParticipantsBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `UPDATE conversations SET participants = '["atlas-ingest"'`)
	assertApplyRefusedWithoutWrite(t, path, loadManifestFixture(t, "success_manifest.csv"), "invalid_participants_json")
}

func TestFailClosedOutsideSurfaceBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	before := snapshotDB(t, path)
	file, err := os.Open(fixturePath("fail_outside_surface.csv"))
	if err != nil {
		t.Fatalf("open outside-surface fixture: %v", err)
	}
	_, err = ParseManifest(file)
	file.Close()
	if code := refusalCode(t, err); code != "outside_rewrite_surface" {
		t.Fatalf("refusal code = %q, want outside_rewrite_surface", code)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("outside-surface refusal changed the database")
	}
}

func TestClosedDispositionVocabulary(t *testing.T) {
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence
jk,worker,personal.worker,retire-unless-confirmed,example/repo,synthetic
`
	_, err := ParseManifest(strings.NewReader(input))
	if code := refusalCode(t, err); code != "invalid_disposition" {
		t.Fatalf("refusal code = %q, want invalid_disposition", code)
	}
}

func TestOtherAuthoritySplitTargetCannotSatisfyJKRun(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, "DELETE FROM idempotency")
	execFixtureSQL(t, path, "DELETE FROM delivery_cursors")
	execFixtureSQL(t, path, "DELETE FROM deliveries")
	execFixtureSQL(t, path, "DELETE FROM messages")
	execFixtureSQL(t, path, "DELETE FROM conversations")
	execFixtureSQL(t, path, "DELETE FROM agents")
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('ucla.dual-canary', 'ucla-secret', 'https://invalid/ucla', 'ucla side', '{}')`)
	assertApplyRefusedWithoutWrite(t, path, loadManifestFixture(t, "success_manifest.csv"), "unknown_identity")
}

func TestCopyAcknowledgmentRequired(t *testing.T) {
	path := createFixtureDB(t)
	before := snapshotDB(t, path)
	_, err := Run(t.Context(), loadManifestFixture(t, "success_manifest.csv"), Options{
		DBPath:    path,
		Authority: AuthorityJK,
		Apply:     true,
	})
	if code := refusalCode(t, err); code != "copy_acknowledgment_required" {
		t.Fatalf("refusal code = %q, want copy_acknowledgment_required", code)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("missing acknowledgment changed the database")
	}
}

func TestApplyRefusesBusyDatabaseBeforeWrite(t *testing.T) {
	path := createFixtureDB(t)
	before := snapshotDB(t, path)

	locker, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	defer locker.Close()
	lock, err := locker.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer lock.Rollback()

	report, err := runApply(t, path, loadManifestFixture(t, "success_manifest.csv"))
	if code := refusalCode(t, err); code != "database_busy" {
		t.Fatalf("refusal code = %q, want database_busy", code)
	}
	if report.Status != "refused" {
		t.Fatalf("report status = %q, want refused", report.Status)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatalf("release lock holder: %v", err)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("database_busy refusal changed the database")
	}
}

func assertApplyRefusedWithoutWrite(t *testing.T, path string, manifest Manifest, wantCode string) {
	t.Helper()
	before := snapshotDB(t, path)
	report, err := runApply(t, path, manifest)
	if err == nil {
		t.Fatalf("expected refusal %q, report %#v", wantCode, report)
	}
	if code := refusalCode(t, err); code != wantCode {
		t.Fatalf("refusal code = %q, want %q", code, wantCode)
	}
	if report.Status != "refused" {
		t.Fatalf("report status = %q, want refused", report.Status)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatalf("refusal %q changed the database", wantCode)
	}
}
