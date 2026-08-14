package bus

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// CompactReport summarizes a db-compact run for operator visibility. All
// page counts come straight from SQLite PRAGMAs, not estimates.
type CompactReport struct {
	DBPath          string
	PageSize        int64
	PageCountBefore int64
	FreePagesBefore int64
	PageCountAfter  int64
	FreePagesAfter  int64
	AutoVacuumMode  string
}

// BytesBefore is the on-disk file size implied by the before-vacuum page
// count (page_size * page_count), which matches what `ls -l` reports for a
// database with no in-flight WAL.
func (r CompactReport) BytesBefore() int64 { return r.PageSize * r.PageCountBefore }

// BytesAfter is the on-disk file size implied by the after-vacuum page
// count.
func (r CompactReport) BytesAfter() int64 { return r.PageSize * r.PageCountAfter }

// CompactSQLiteDB reclaims free pages from a bus SQLite database and leaves
// it in incremental auto_vacuum mode so the SQLiteStore's periodic
// PRAGMA incremental_vacuum (see pruneDB in sqlite.go) can keep it from
// re-bloating going forward.
//
// This function is meant to run against a database whose owning bus process
// is stopped. It does not start or stop any process itself, and it refuses
// to proceed if it cannot get an immediate write lock on the file (that
// means something else, most likely a running bus, already has it open) —
// callers should treat that refusal as "stop the bus and retry", not
// something to work around by waiting. It never opens a live/production
// database that a caller has not already confirmed is stopped.
func CompactSQLiteDB(dbPath string) (CompactReport, error) {
	report := CompactReport{DBPath: dbPath}

	info, err := os.Lstat(dbPath)
	if err != nil {
		return report, fmt.Errorf("stat database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return report, fmt.Errorf("database path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return report, fmt.Errorf("database path must be a regular file")
	}

	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return report, fmt.Errorf("resolve database path: %w", err)
	}
	// busy_timeout(0) means the write-lock probe below fails immediately
	// instead of blocking, so a locked (i.e. still-running-bus) database
	// surfaces as a clear error rather than a hang.
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() +
		"?_pragma=busy_timeout(0)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return report, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		if isSQLiteBusy(err) {
			return report, fmt.Errorf("database is locked by another process — stop the bus before compacting: %w", err)
		}
		return report, fmt.Errorf("acquire write lock: %w", err)
	}
	if _, err := db.Exec("COMMIT"); err != nil {
		return report, fmt.Errorf("release write-lock probe: %w", err)
	}

	if err := db.Get(&report.PageSize, "PRAGMA page_size"); err != nil {
		return report, fmt.Errorf("read page_size: %w", err)
	}
	if err := db.Get(&report.PageCountBefore, "PRAGMA page_count"); err != nil {
		return report, fmt.Errorf("read page_count: %w", err)
	}
	if err := db.Get(&report.FreePagesBefore, "PRAGMA freelist_count"); err != nil {
		return report, fmt.Errorf("read freelist_count: %w", err)
	}

	// auto_vacuum only takes effect for tables created after it is set, so on
	// an existing file it stays inert until the VACUUM below rewrites the
	// whole database — at which point it both reclaims every current free
	// page and converts the file to incremental mode in one step.
	if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return report, fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		return report, fmt.Errorf("vacuum: %w", err)
	}

	if err := db.Get(&report.PageCountAfter, "PRAGMA page_count"); err != nil {
		return report, fmt.Errorf("read page_count after vacuum: %w", err)
	}
	if err := db.Get(&report.FreePagesAfter, "PRAGMA freelist_count"); err != nil {
		return report, fmt.Errorf("read freelist_count after vacuum: %w", err)
	}
	var mode int
	if err := db.Get(&mode, "PRAGMA auto_vacuum"); err != nil {
		return report, fmt.Errorf("read auto_vacuum: %w", err)
	}
	switch mode {
	case 0:
		report.AutoVacuumMode = "none"
	case 1:
		report.AutoVacuumMode = "full"
	case 2:
		report.AutoVacuumMode = "incremental"
	default:
		report.AutoVacuumMode = fmt.Sprintf("unknown(%d)", mode)
	}

	return report, nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}
