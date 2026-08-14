// pinakes-db-compact reclaims free-page bloat from a bus SQLite database
// (issue #29: a live bus DB was found 99.86% free pages, ~1.17 GB holding
// ~1.6 MB of live data). It is a one-time/occasional maintenance tool for an
// operator to run against a STOPPED bus, not something the bus runs on
// itself — the bus's own prune loop only ever reclaims pages incrementally
// (see PRAGMA incremental_vacuum in pkg/bus/sqlite.go), it never VACUUMs.
//
// Usage:
//
//	pinakes-db-compact --db /path/to/bus.db --acknowledge-stopped
//
// The bus process that owns --db MUST already be stopped. This tool takes
// an immediate write lock as a best-effort check (it fails fast, rather than
// hanging, if something else still has the file open) but that is a safety
// net, not a substitute for actually stopping the bus first.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pinakes-db-compact", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "path to the bus SQLite database file (its bus process must be stopped)")
	acknowledgeStopped := flags.Bool(
		"acknowledge-stopped",
		false,
		"acknowledge that the bus process owning --db is stopped",
	)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: pinakes-db-compact --db PATH --acknowledge-stopped")
		fmt.Fprintln(stderr, "Stop the bus process that owns --db before running this.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		flags.Usage()
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stderr, "--db is required")
		flags.Usage()
		return 2
	}
	if !*acknowledgeStopped {
		fmt.Fprintln(stderr, "refusing to run without --acknowledge-stopped: stop the bus process that owns --db first")
		flags.Usage()
		return 2
	}

	report, err := bus.CompactSQLiteDB(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "compact failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "db:                %s\n", report.DBPath)
	fmt.Fprintf(stdout, "page size:         %d bytes\n", report.PageSize)
	fmt.Fprintf(stdout, "before: pages=%d free=%d size=%.2f MB\n",
		report.PageCountBefore, report.FreePagesBefore, float64(report.BytesBefore())/(1024*1024))
	fmt.Fprintf(stdout, "after:  pages=%d free=%d size=%.2f MB\n",
		report.PageCountAfter, report.FreePagesAfter, float64(report.BytesAfter())/(1024*1024))
	fmt.Fprintf(stdout, "auto_vacuum mode:  %s\n", report.AutoVacuumMode)
	return 0
}
