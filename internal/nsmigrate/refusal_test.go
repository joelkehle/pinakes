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
		"jk,ucla.foreign-worker,ucla.foreign-worker,unchanged,example/repo,synthetic foreign scope,false\n"))
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
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,worker,personal.worker,retire-unless-confirmed,example/repo,synthetic,false
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

func TestForeignAuthorityUnchangedIsAllowedAndPreserved(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('ucla.foreign-worker', 'foreign-secret', 'https://invalid/foreign', 'foreign', '{}')`)

	manifest := manifestWithExtraRow(t,
		"jk,ucla.foreign-worker,ucla.foreign-worker,unchanged,example/repo,synthetic foreign scope,false\n")
	report, err := runApply(t, path, manifest)
	if err != nil {
		t.Fatalf("apply with foreign unchanged identity: %v", err)
	}
	if report.Status != "applied" {
		t.Fatalf("report status = %q, want applied", report.Status)
	}

	database, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatalf("open applied database: %v", err)
	}
	defer database.Close()
	assertStrings(t, database,
		"SELECT agent_id || '|' || secret FROM agents WHERE agent_id = 'ucla.foreign-worker'",
		[]string{"ucla.foreign-worker|foreign-secret"},
	)
}

func TestJKManifestRefusesUCLAMutationWithoutWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('ucla.foreign-worker', 'foreign-secret', 'https://invalid/foreign', 'foreign', '{}')`)
	assertForeignManifestRefusedWithoutWrite(t, path,
		"jk,ucla.foreign-worker,personal.ucla-foreign-worker,migrate,example/repo,synthetic foreign mutation,false\n")
}

func TestUCLAManifestRefusesPersonalMutationWithoutWrite(t *testing.T) {
	path := createFixtureDB(t)
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('personal.foreign-worker', 'foreign-secret', 'https://invalid/foreign', 'foreign', '{}')`)
	assertForeignManifestRefusedWithoutWrite(t, path,
		"ucla,personal.foreign-worker,ucla.personal-foreign-worker,migrate,example/repo,synthetic foreign mutation,false\n")
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

func TestControlPlaneManifestMustMatchConfiguration(t *testing.T) {
	manifest := parseManifestText(t, `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,managerd,managerd,unchanged,example/manager,synthetic control plane,true
`)
	path := createFixtureDB(t)
	before := snapshotDB(t, path)
	report, err := Run(t.Context(), manifest, Options{
		DBPath:          path,
		Authority:       AuthorityJK,
		Apply:           true,
		AcknowledgeCopy: true,
	})
	if code := refusalCode(t, err); code != "control_plane_mismatch" {
		t.Fatalf("refusal code = %q, want control_plane_mismatch", code)
	}
	if report.Status != "refused" {
		t.Fatalf("report status = %q, want refused", report.Status)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("control-plane mismatch changed the database")
	}
}

func TestConfiguredControlPlaneDoesNotRequireMarkedAuthorityRow(t *testing.T) {
	path := createFixtureDB(t)
	report, err := Run(t.Context(), loadManifestFixture(t, "success_manifest.csv"), Options{
		DBPath:             path,
		Authority:          AuthorityJK,
		AcknowledgeCopy:    true,
		ControlPlaneAgents: []string{"managerd"},
	})
	if err != nil {
		t.Fatalf("configured identity outside this authority should not refuse: %v", err)
	}
	if report.Status != "ready" {
		t.Fatalf("report status = %q, want ready", report.Status)
	}
}

func TestControlPlaneManifestAndConfigurationAgree(t *testing.T) {
	manifest := parseManifestText(t, `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,managerd,managerd,unchanged,example/manager,synthetic control plane,true
`)
	path := createFixtureDB(t)
	execFixtureSQL(t, path, "DELETE FROM idempotency")
	execFixtureSQL(t, path, "DELETE FROM delivery_cursors")
	execFixtureSQL(t, path, "DELETE FROM deliveries")
	execFixtureSQL(t, path, "DELETE FROM messages")
	execFixtureSQL(t, path, "DELETE FROM conversations")
	execFixtureSQL(t, path, "DELETE FROM agents")
	execFixtureSQL(t, path, `INSERT INTO agents VALUES
		('managerd', 'synthetic-secret', 'https://invalid/managerd', 'synthetic control plane', '{}')`)
	report, err := Run(t.Context(), manifest, Options{
		DBPath:             path,
		Authority:          AuthorityJK,
		AcknowledgeCopy:    true,
		ControlPlaneAgents: []string{"managerd"},
	})
	if err != nil {
		t.Fatalf("matching control-plane rehearsal: %v", err)
	}
	if report.Status != "no-op" {
		t.Fatalf("report status = %q, want no-op", report.Status)
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

func assertForeignManifestRefusedWithoutWrite(t *testing.T, path, row string) {
	t.Helper()
	before := snapshotDB(t, path)
	_, err := ParseManifest(strings.NewReader(readManifestFixture(t, "success_manifest.csv") + row))
	if code := refusalCode(t, err); code != "foreign_authority_mutation" {
		t.Fatalf("refusal code = %q, want foreign_authority_mutation", code)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("foreign_authority_mutation refusal changed the database")
	}
}

func manifestWithExtraRow(t *testing.T, row string) Manifest {
	t.Helper()
	manifest, err := ParseManifest(strings.NewReader(readManifestFixture(t, "success_manifest.csv") + row))
	if err != nil {
		t.Fatalf("parse manifest with extra row: %v", err)
	}
	return manifest
}

func parseManifestText(t *testing.T, input string) Manifest {
	t.Helper()
	manifest, err := ParseManifest(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return manifest
}

func readManifestFixture(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatalf("read manifest fixture: %v", err)
	}
	return string(contents)
}
