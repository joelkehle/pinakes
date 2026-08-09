package nsmigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func fixturePath(parts ...string) string {
	return filepath.Join(append([]string{"testdata"}, parts...)...)
}

func loadManifestFixture(t *testing.T, name string) Manifest {
	t.Helper()
	file, err := os.Open(fixturePath(name))
	if err != nil {
		t.Fatalf("open manifest fixture: %v", err)
	}
	defer file.Close()
	manifest, err := ParseManifest(file)
	if err != nil {
		t.Fatalf("parse manifest fixture: %v", err)
	}
	return manifest
}

func createFixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rehearsal-copy.db")
	schema, err := os.ReadFile(fixturePath("success.sql"))
	if err != nil {
		t.Fatalf("read SQL fixture: %v", err)
	}
	database, err := sql.Open("sqlite", path)
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
	return path
}

func execFixtureSQL(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer database.Close()
	if _, err := database.Exec(statement, args...); err != nil {
		t.Fatalf("execute fixture SQL: %v", err)
	}
}

func snapshotDB(t *testing.T, path string) string {
	t.Helper()
	database, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatalf("open snapshot database: %v", err)
	}
	defer database.Close()

	queries := []string{
		"SELECT * FROM agents ORDER BY agent_id",
		"SELECT * FROM conversations ORDER BY conversation_id",
		"SELECT * FROM messages ORDER BY message_id",
		"SELECT * FROM deliveries ORDER BY target_agent_id, delivery_seq",
		"SELECT * FROM delivery_cursors ORDER BY target_agent_id",
		"SELECT * FROM idempotency ORDER BY from_agent, to_agent, request_id",
	}
	var snapshot strings.Builder
	for _, query := range queries {
		rows, err := database.Query(query)
		if err != nil {
			t.Fatalf("snapshot query %q: %v", query, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("snapshot columns: %v", err)
		}
		fmt.Fprintf(&snapshot, "%s\n", query)
		for rows.Next() {
			raw := make([]sql.RawBytes, len(columns))
			dest := make([]any, len(columns))
			for index := range raw {
				dest[index] = &raw[index]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatalf("snapshot scan: %v", err)
			}
			for index, value := range raw {
				if index > 0 {
					snapshot.WriteByte('\x1f')
				}
				if value == nil {
					snapshot.WriteString("<NULL>")
				} else {
					snapshot.Write(value)
				}
			}
			snapshot.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("snapshot rows: %v", err)
		}
		rows.Close()
	}
	return snapshot.String()
}

func runApply(t *testing.T, path string, manifest Manifest) (Report, error) {
	t.Helper()
	return Run(context.Background(), manifest, Options{
		DBPath:          path,
		Authority:       AuthorityJK,
		Apply:           true,
		AcknowledgeCopy: true,
	})
}

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	refusal, ok := err.(*RefusalError)
	if !ok || len(refusal.Refusals) == 0 {
		t.Fatalf("expected RefusalError, got %T: %v", err, err)
	}
	return refusal.Refusals[0].Code
}

func inverseManifest(t *testing.T, manifest Manifest) Manifest {
	t.Helper()
	inverse, err := InvertManifest(manifest)
	if err != nil {
		t.Fatalf("invert manifest: %v", err)
	}
	return inverse
}

func formattedReport(t *testing.T, report Report) []byte {
	t.Helper()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return append(encoded, '\n')
}
