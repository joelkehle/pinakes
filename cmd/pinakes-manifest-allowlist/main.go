package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/joelkehle/pinakes/internal/nsmigrate"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("pinakes-manifest-allowlist", flag.ContinueOnError)
	manifestPath := flags.String("manifest", "", "path to the reviewed migration manifest")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "usage: pinakes-manifest-allowlist --manifest manifest.csv")
		return 2
	}
	input, err := os.Open(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open manifest: %v\n", err)
		return 1
	}
	manifest, parseErr := nsmigrate.ParseManifest(input)
	closeErr := input.Close()
	if parseErr != nil {
		fmt.Fprintln(os.Stderr, parseErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "close manifest: %v\n", closeErr)
		return 1
	}

	identities := map[string]struct{}{}
	for _, row := range manifest.Rows {
		if row.Disposition == nsmigrate.DispositionRetire {
			continue
		}
		identities[row.TargetID] = struct{}{}
	}
	ordered := make([]string, 0, len(identities))
	for identity := range identities {
		ordered = append(ordered, identity)
	}
	sort.Strings(ordered)

	writer := bufio.NewWriter(os.Stdout)
	fmt.Fprintln(writer, "# Proposed post-migration Pinakes agent allowlist")
	fmt.Fprintln(writer, "# Generated from the reviewed issue #15 namespace manifest.")
	fmt.Fprintln(writer, "# Retired identities are intentionally absent.")
	for _, identity := range ordered {
		fmt.Fprintln(writer, identity)
	}
	if err := writer.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "write allowlist: %v\n", err)
		return 1
	}
	return 0
}
