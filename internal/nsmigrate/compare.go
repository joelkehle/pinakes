package nsmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type LogicalComparisonReport struct {
	Equal            bool     `json:"equal"`
	TableCount       int      `json:"table_count"`
	Tables           []string `json:"tables"`
	MismatchedTables []string `json:"mismatched_tables,omitempty"`
}

type logicalTable struct {
	name   string
	schema string
	digest [sha256.Size]byte
}

// CompareLogicalDatabases compares schema and canonically ordered rows for
// every non-SQLite-internal table. It never returns row data or digests.
func CompareLogicalDatabases(ctx context.Context, expectedPath, actualPath string) (LogicalComparisonReport, error) {
	report := LogicalComparisonReport{}
	if err := validateDBPath(expectedPath); err != nil {
		return report, fmt.Errorf("expected database: %w", err)
	}
	if err := validateDBPath(actualPath); err != nil {
		return report, fmt.Errorf("actual database: %w", err)
	}
	expected, err := logicalTables(ctx, expectedPath)
	if err != nil {
		return report, fmt.Errorf("read expected database: %w", err)
	}
	actual, err := logicalTables(ctx, actualPath)
	if err != nil {
		return report, fmt.Errorf("read actual database: %w", err)
	}

	names := map[string]struct{}{}
	for name := range expected {
		names[name] = struct{}{}
	}
	for name := range actual {
		names[name] = struct{}{}
	}
	for name := range names {
		report.Tables = append(report.Tables, name)
	}
	sort.Strings(report.Tables)
	report.TableCount = len(report.Tables)
	for _, name := range report.Tables {
		left, leftOK := expected[name]
		right, rightOK := actual[name]
		if !leftOK || !rightOK || left.schema != right.schema || left.digest != right.digest {
			report.MismatchedTables = append(report.MismatchedTables, name)
		}
	}
	report.Equal = len(report.MismatchedTables) == 0
	return report, nil
}

func logicalTables(ctx context.Context, path string) (map[string]logicalTable, error) {
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=foreign_keys(1)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer database.Close()

	rows, err := database.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	tables := map[string]logicalTable{}
	for rows.Next() {
		var name, schema string
		if err := rows.Scan(&name, &schema); err != nil {
			rows.Close()
			return nil, err
		}
		tables[name] = logicalTable{name: name, schema: schema}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for name, table := range tables {
		digest, err := logicalTableDigest(ctx, database, table)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", name, err)
		}
		table.digest = digest
		tables[name] = table
	}
	return tables, nil
}

func logicalTableDigest(ctx context.Context, database *sql.DB, table logicalTable) ([sha256.Size]byte, error) {
	columns, err := tableColumns(ctx, database, table.name)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	if err := encoder.Encode(table.schema); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := encoder.Encode(columns); err != nil {
		return [sha256.Size]byte{}, err
	}
	if len(columns) == 0 {
		var digest [sha256.Size]byte
		copy(digest[:], hash.Sum(nil))
		return digest, nil
	}

	expressions := make([]string, len(columns))
	for index, column := range columns {
		expressions[index] = "quote(" + quoteIdentifier(column) + ")"
	}
	query := "SELECT " + strings.Join(expressions, ",") + " FROM " + quoteIdentifier(table.name) +
		" ORDER BY " + strings.Join(expressions, ",")
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]string, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return [sha256.Size]byte{}, err
		}
		if err := encoder.Encode(values); err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func tableColumns(ctx context.Context, database *sql.DB, table string) ([]string, error) {
	rows, err := database.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
