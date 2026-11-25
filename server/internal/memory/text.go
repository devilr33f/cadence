package memory

import "strings"

// how stored text enters a prompt or the context, the same everywhere: a
// block (a summary, a message) keeps its lines, indented; an item (a thread,
// a memory) is one line

var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n", "\u0085", "\n")

// IndentLines indents every line after the first, so text inside a block can
// never pass for a line of its own ("[12] User: ...", a header).
func IndentLines(s string) string {
	return strings.ReplaceAll(lineBreaks.Replace(s), "\n", "\n    ")
}

// OneLine collapses every run of whitespace, line breaks included, into one space.
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
