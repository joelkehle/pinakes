package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/joelkehle/pinakes/internal/nsmigrate"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("pinakes-migrate-compare", flag.ContinueOnError)
	expected := flags.String("expected", "", "pristine expected SQLite database")
	actual := flags.String("actual", "", "inverse-restored SQLite database")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *expected == "" || *actual == "" {
		fmt.Fprintln(os.Stderr, "usage: pinakes-migrate-compare --expected PRISTINE.db --actual RESTORED.db")
		return 2
	}
	report, err := nsmigrate.CompareLogicalDatabases(ctx, *expected, *actual)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "write comparison report: %v\n", err)
		return 1
	}
	if !report.Equal {
		return 1
	}
	return 0
}
