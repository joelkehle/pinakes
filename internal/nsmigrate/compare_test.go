package nsmigrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func TestCompareLogicalDatabases(t *testing.T) {
	directory := t.TempDir()
	expectedPath := filepath.Join(directory, "expected.db")
	actualPath := filepath.Join(directory, "actual.db")
	store, err := bus.NewSQLiteStore(expectedPath, bus.Config{})
	if err != nil {
		t.Fatalf("create expected database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close expected database: %v", err)
	}
	contents, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read expected database: %v", err)
	}
	if err := os.WriteFile(actualPath, contents, 0o600); err != nil {
		t.Fatalf("write actual database: %v", err)
	}

	report, err := CompareLogicalDatabases(t.Context(), expectedPath, actualPath)
	if err != nil {
		t.Fatalf("compare equal databases: %v", err)
	}
	if !report.Equal || report.TableCount != 9 || len(report.MismatchedTables) != 0 {
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
