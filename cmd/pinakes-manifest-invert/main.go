package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/joelkehle/pinakes/internal/nsmigrate"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("pinakes-manifest-invert", flag.ContinueOnError)
	inputPath := flags.String("manifest", "", "path to the reviewed forward manifest")
	outputPath := flags.String("output", "", "new path for the exact inverse manifest")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *inputPath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "usage: pinakes-manifest-invert --manifest manifest.csv --output NEW.csv")
		return 2
	}
	input, err := os.Open(*inputPath)
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
	inverse, err := nsmigrate.InvertManifest(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invert manifest: %v\n", err)
		return 1
	}
	output, err := os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create inverse manifest: %v\n", err)
		return 1
	}
	writeErr := nsmigrate.WriteManifest(output, inverse)
	closeErr = output.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(*outputPath)
		if writeErr != nil {
			fmt.Fprintf(os.Stderr, "write inverse manifest: %v\n", writeErr)
		} else {
			fmt.Fprintf(os.Stderr, "close inverse manifest: %v\n", closeErr)
		}
		return 1
	}
	return 0
}
