package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/joelkehle/pinakes/internal/nsmigrate"
	_ "modernc.org/sqlite"
)

func TestRunDryRunThenApply(t *testing.T) {
	dbPath := createCLIFixture(t)
	manifestPath := filepath.Join("..", "..", "internal", "nsmigrate", "testdata", "success_manifest.csv")

	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"--db", dbPath,
		"--manifest", manifestPath,
		"--authority", "jk",
		"--acknowledge-copy",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("dry-run exit=%d stderr=%s stdout=%s", exitCode, stderr.String(), stdout.String())
	}
	var dryRun nsmigrate.Report
	if err := json.Unmarshal(stdout.Bytes(), &dryRun); err != nil {
		t.Fatalf("decode dry-run report: %v", err)
	}
	if dryRun.Status != "ready" || dryRun.Mode != "dry-run" {
		t.Fatalf("dry-run report = %#v", dryRun)
	}
	assertAgentExists(t, dbPath, "atlas-ingest")

	stdout.Reset()
	stderr.Reset()
	exitCode = run(context.Background(), []string{
		"--db", dbPath,
		"--manifest", manifestPath,
		"--authority", "jk",
		"--acknowledge-copy",
		"--apply",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("apply exit=%d stderr=%s stdout=%s", exitCode, stderr.String(), stdout.String())
	}
	var apply nsmigrate.Report
	if err := json.Unmarshal(stdout.Bytes(), &apply); err != nil {
		t.Fatalf("decode apply report: %v", err)
	}
	if apply.Status != "applied" || apply.Mode != "apply" {
		t.Fatalf("apply report = %#v", apply)
	}
	assertAgentExists(t, dbPath, "personal.atlas-ingest")
}

func createCLIFixture(t *testing.T) string {
	t.Helper()
	sqlPath := filepath.Join("..", "..", "internal", "nsmigrate", "testdata", "success.sql")
	schema, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read fixture SQL: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "copy.db")
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err := database.Exec(string(schema)); err != nil {
		database.Close()
		t.Fatalf("create fixture database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	return dbPath
}

func assertAgentExists(t *testing.T, dbPath, agentID string) {
	t.Helper()
	database, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM agents WHERE agent_id = ?", agentID).Scan(&count); err != nil {
		t.Fatalf("query agent: %v", err)
	}
	if count != 1 {
		t.Fatalf("agent %q count=%d, want 1", agentID, count)
	}
}
