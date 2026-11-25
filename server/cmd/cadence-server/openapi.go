package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"git.shino.boo/devilreef/cadence/server/internal/app"
)

// specVersion is fixed so the committed document does not drift per commit
const specVersion = "0.0.0-dev"

// runOpenAPI writes the admin OpenAPI document to stdout or the -o file. It
// builds the api without a database or config.
func runOpenAPI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin openapi", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("o", "", "write to this file instead of stdout")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: cadence-server admin openapi [-o path]")
		return 2
	}
	doc, err := app.AdminSpec(specVersion)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "openapi: build failed")
		return 1
	}
	if *out == "" {
		_, _ = stdout.Write(doc)
		return 0
	}
	if err := os.WriteFile(*out, doc, 0o600); err != nil {
		_, _ = fmt.Fprintln(stderr, "openapi: write failed")
		return 1
	}
	return 0
}
