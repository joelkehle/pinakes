package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/joelkehle/pinakes/internal/nsmigrate"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pinakes-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "path to a stopped SQLite database copy")
	manifestPath := flags.String("manifest", "", "path to the reviewed CSV manifest")
	authorityRaw := flags.String("authority", "", "authority of this database copy: jk or ucla")
	apply := flags.Bool("apply", false, "apply the planned rewrite (default is dry-run)")
	acknowledgeCopy := flags.Bool(
		"acknowledge-copy",
		false,
		"acknowledge that --db is a stopped or transactional rehearsal copy",
	)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: pinakes-migrate --db COPY.db --manifest manifest.csv --authority jk|ucla --acknowledge-copy [--apply]")
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

	authority, err := nsmigrate.ParseAuthority(*authorityRaw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *manifestPath == "" {
		fmt.Fprintln(stderr, "--manifest is required")
		return 2
	}
	manifestFile, err := os.Open(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "open manifest: %v\n", err)
		return 1
	}
	manifest, parseErr := nsmigrate.ParseManifest(manifestFile)
	closeErr := manifestFile.Close()
	if parseErr != nil {
		writeParseRefusal(stdout, authority, *apply, parseErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(stderr, "close manifest: %v\n", closeErr)
		return 1
	}

	report, runErr := nsmigrate.Run(ctx, manifest, nsmigrate.Options{
		DBPath:          *dbPath,
		Authority:       authority,
		Apply:           *apply,
		AcknowledgeCopy: *acknowledgeCopy,
	})
	if err := writeReport(stdout, report); err != nil {
		fmt.Fprintf(stderr, "write report: %v\n", err)
		return 1
	}
	if runErr != nil {
		return 1
	}
	return 0
}

func writeParseRefusal(output io.Writer, authority nsmigrate.Authority, apply bool, err error) {
	refusals := []nsmigrate.Refusal{{
		Code:     "manifest_schema",
		Location: "manifest",
		Detail:   err.Error(),
	}}
	var refusalError *nsmigrate.RefusalError
	if errors.As(err, &refusalError) {
		refusals = refusalError.Refusals
	}
	_ = writeReport(output, nsmigrate.RefusedReport(authority, apply, refusals))
}

func writeReport(output io.Writer, report nsmigrate.Report) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
