package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/joelkehle/pinakes/internal/configutil"
	"github.com/joelkehle/pinakes/internal/nsmigrate"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("pinakes-migrate-fixture", flag.ContinueOnError)
	dbPath := flags.String("db", "", "new path for the generated synthetic SQLite database")
	manifestPath := flags.String("manifest", "", "path to the reviewed migration manifest")
	authorityRaw := flags.String("authority", "", "fixture authority: jk or ucla")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *dbPath == "" || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "usage: pinakes-migrate-fixture --db NEW.db --manifest manifest.csv --authority jk|ucla")
		return 2
	}
	authority, err := nsmigrate.ParseAuthority(*authorityRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	manifestFile, err := os.Open(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open manifest: %v\n", err)
		return 1
	}
	manifest, parseErr := nsmigrate.ParseManifest(manifestFile)
	closeErr := manifestFile.Close()
	if parseErr != nil {
		fmt.Fprintln(os.Stderr, parseErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "close manifest: %v\n", closeErr)
		return 1
	}
	report, err := nsmigrate.GenerateSyntheticDB(ctx, manifest, nsmigrate.SyntheticOptions{
		DBPath:             *dbPath,
		Authority:          authority,
		ControlPlaneAgents: configutil.SplitCSV(os.Getenv("CONTROL_PLANE_AGENTS")),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		return 1
	}
	return 0
}
