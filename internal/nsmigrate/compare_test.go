package nsmigrate

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func TestCompareLogicalDatabases(t *testing.T) {
	directory := t.TempDir()
	expectedPath := filepath.Join(directory, "expected.db")
	actualPath := filepath.Join(directory, "actual.db")
	fixedNow := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, path := range []string{expectedPath, actualPath} {
		store, err := bus.NewSQLiteStore(path, bus.Config{
			Clock: func() time.Time { return fixedNow },
		})
		if err != nil {
			t.Fatalf("create independent database %s: %v", filepath.Base(path), err)
		}
		if _, err := store.RegisterAgent(bus.RegisterAgentInput{
			AgentID:    "ucla.synthetic-worker",
			Secret:     "fabricated-secret",
			Mode:       bus.AgentModePull,
			TTLSeconds: 60,
		}); err != nil {
			store.Close()
			t.Fatalf("populate independent database %s: %v", filepath.Base(path), err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close independent database %s: %v", filepath.Base(path), err)
		}
	}

	// Exercise canonical BLOB ordering independently of physical insertion order.
	execFixtureSQL(t, expectedPath, `CREATE TABLE logical_blob_fixture (id INTEGER PRIMARY KEY, payload BLOB NOT NULL)`)
	execFixtureSQL(t, actualPath, `CREATE TABLE logical_blob_fixture (id INTEGER PRIMARY KEY, payload BLOB NOT NULL)`)
	execFixtureSQL(t, expectedPath, `INSERT INTO logical_blob_fixture(id, payload) VALUES (?, ?), (?, ?)`, 1, []byte{0xff, 0x00}, 2, []byte{0x01, 0x02})
	execFixtureSQL(t, actualPath, `INSERT INTO logical_blob_fixture(id, payload) VALUES (?, ?), (?, ?)`, 2, []byte{0x01, 0x02}, 1, []byte{0xff, 0x00})

	report, err := CompareLogicalDatabases(t.Context(), expectedPath, actualPath)
	if err != nil {
		t.Fatalf("compare equal databases: %v", err)
	}
	wantTableCount := logicalTableCount(t, expectedPath)
	if !report.Equal || report.TableCount != wantTableCount || len(report.Tables) != wantTableCount || len(report.MismatchedTables) != 0 {
		t.Fatalf("equal report = %#v", report)
	}

	execFixtureSQL(t, actualPath, `INSERT INTO counters(key, value) VALUES ('synthetic-difference', 1)`)
	report, err = CompareLogicalDatabases(t.Context(), expectedPath, actualPath)
	if err != nil {
		t.Fatalf("compare different databases: %v", err)
	}
	if report.Equal || len(report.MismatchedTables) != 1 || report.MismatchedTables[0] != "counters" {
		t.Fatalf("different report = %#v", report)
	}
}

func logicalTableCount(t *testing.T, path string) int {
	t.Helper()
	database, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatalf("open database for schema count: %v", err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		t.Fatalf("count logical tables: %v", err)
	}
	return count
}
