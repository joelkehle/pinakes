package bus

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// buildLegacyBloatedDB creates a SQLite file the way pre-fix pinakes bus DBs
// were created: no auto_vacuum, a table churned with inserts and deletes and
// never VACUUMed. This reproduces the shape of issue #29 (a file whose
// freelist holds almost all of its pages) without touching any real bus
// database.
func buildLegacyBloatedDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE junk (id INTEGER PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	payload := strings.Repeat("x", 4000) // ~one page of body per row
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin insert tx: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO junk (body) VALUES (?)`)
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}
	for i := 0; i < 2000; i++ {
		if _, err := stmt.Exec(payload); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit insert tx: %v", err)
	}

	// Delete almost everything: SQLite returns these pages to the freelist
	// but keeps the file at its high-water mark size without a VACUUM.
	if _, err := db.Exec(`DELETE FROM junk WHERE id % 100 <> 0`); err != nil {
		t.Fatalf("delete rows: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
	return dbPath
}

func TestCompactSQLiteDBReclaimsFreePages(t *testing.T) {
	dbPath := buildLegacyBloatedDB(t)

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	report, err := CompactSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	if report.FreePagesBefore == 0 {
		t.Fatalf("expected the synthetic legacy DB to have free pages before compacting, got 0")
	}
	if report.FreePagesAfter != 0 {
		t.Fatalf("expected 0 free pages after VACUUM, got %d", report.FreePagesAfter)
	}
	if report.PageCountAfter >= report.PageCountBefore {
		t.Fatalf("expected page_count to shrink: before=%d after=%d", report.PageCountBefore, report.PageCountAfter)
	}
	if report.AutoVacuumMode != "incremental" {
		t.Fatalf("expected auto_vacuum mode incremental after compact, got %q", report.AutoVacuumMode)
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("expected on-disk file to shrink: before=%d after=%d", before.Size(), after.Size())
	}
	// The compacted file should be much closer to its live data (~20 rows)
	// than to its pre-compact high-water mark (2000 rows).
	if after.Size() > before.Size()/2 {
		t.Fatalf("expected compacted file well under half its bloated size: before=%d after=%d", before.Size(), after.Size())
	}

	// A second compact run on an already-tight database should be a
	// well-behaved no-op, not an error.
	report2, err := CompactSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("second compact: %v", err)
	}
	if report2.FreePagesAfter != 0 {
		t.Fatalf("expected 0 free pages after second compact, got %d", report2.FreePagesAfter)
	}
}

func TestCompactSQLiteDBRefusesMissingPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "does-not-exist.db")
	if _, err := CompactSQLiteDB(dbPath); err == nil {
		t.Fatalf("expected error compacting a nonexistent database")
	}
}

// TestCompactSQLiteDBRefusesLockedDB proves the write-lock probe fails fast
// (rather than hanging or silently vacuuming under a live writer) when
// something else holds a write transaction open on the file — standing in
// for "the bus process was not actually stopped before running the tool".
func TestCompactSQLiteDBRefusesLockedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "locked.db")

	setup, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := setup.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup connection: %v", err)
	}

	holder, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holder.Close()
	if _, err := holder.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	defer holder.Exec("ROLLBACK")

	if _, err := CompactSQLiteDB(dbPath); err == nil {
		t.Fatalf("expected compact to refuse a database with an open write lock")
	} else if !strings.Contains(err.Error(), "stop the bus") {
		t.Fatalf("expected a 'stop the bus' hint in the error, got: %v", err)
	}
}
