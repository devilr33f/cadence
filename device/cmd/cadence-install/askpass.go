package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// askpass is ssh's SSH_ASKPASS hook: the installer re-executes itself and prints the password file's first line, the
// only place the file is read. the messages name neither the path nor the content
func askpass(file string, stdout, stderr io.Writer) int {
	b, err := os.ReadFile(file) //nolint:gosec // the owner's own password file
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cadence-install: askpass: the password file is unreadable")
		return exitFailed
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	_, _ = stdout.Write(bytes.TrimRight(line, "\r"))
	return 0
}
