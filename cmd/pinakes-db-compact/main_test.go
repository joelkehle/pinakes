package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRunRequiresAcknowledgeStopped(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	createFixtureDB(t, dbPath)

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--db", dbPath}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("expected exit code 2 without --acknowledge-stopped, got %d (stderr=%s)", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "acknowledge-stopped") {
		t.Fatalf("expected refusal message about --acknowledge-stopped, got: %s", stderr.String())
	}
}

func TestRunRequiresDBFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--acknowledge-stopped"}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("expected exit code 2 without --db, got %d (stderr=%s)", exitCode, stderr.String())
	}
}

func TestRunCompactsFixtureDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bus.db")
	createFixtureDB(t, dbPath)

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--db", dbPath, "--acknowledge-stopped"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("compact exit=%d stderr=%s stdout=%s", exitCode, stderr.String(), stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{"before:", "after:", "auto_vacuum mode:  incremental"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got: %s", want, out)
		}
	}
}

func createFixtureDB(t *testing.T, dbPath string) {
	t.Helper()
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE junk (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO junk (body) VALUES (?)`, strings.Repeat("z", 4000)); err != nil {
		t.Fatalf("insert fixture row: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM junk`); err != nil {
		t.Fatalf("delete fixture row: %v", err)
	}
}
