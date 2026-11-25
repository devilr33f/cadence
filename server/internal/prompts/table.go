// Package prompts renders the device's spoken prompts (offline, failed, not_heard, reminder) through a TTS provider
// instance into the Ogg Opus files audiod plays from share/prompts/<lang>/<id>.opus. The owner runs it once, with the
// server's own configured provider, so no key leaves the server (rule 6); the files are never committed.
package prompts

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

//go:embed prompts.tsv
var table string

// Prompt is one phrase of the table.
type Prompt struct {
	ID, Lang, Text string
}

var (
	idRe   = regexp.MustCompile(`^[a-z_]{1,32}$`)
	langRe = regexp.MustCompile(`^[a-z]{2,3}$`)
)

// maxText bounds a phrase: a prompt is a few words, at most 3 s on the device
const maxText = 200

// ErrTable is a table that does not parse: the line is named, never its text.
var ErrTable = errors.New("prompts: invalid table")

// Table is the built-in table.
func Table() ([]Prompt, error) { return Parse(strings.NewReader(table)) }

// Parse reads a tab-separated table: a header line "id\tlang\ttext", then one prompt a line; blank lines and lines
// starting with # are skipped. An id is [a-z_] (at most 32), a lang [a-z] (2 or 3), each pair once.
func Parse(r io.Reader) ([]Prompt, error) {
	sc := bufio.NewScanner(r)
	var out []Prompt
	seen := map[string]bool{}
	line, header := 0, false
	for sc.Scan() {
		line++
		text := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if !header {
			if text != "id\tlang\ttext" {
				return nil, fmt.Errorf("%w: line %d: the header must be id, lang, text", ErrTable, line)
			}
			header = true
			continue
		}
		f := strings.Split(text, "\t")
		if len(f) != 3 || !idRe.MatchString(f[0]) || !langRe.MatchString(f[1]) {
			return nil, fmt.Errorf("%w: line %d: want id, lang and text, tab separated", ErrTable, line)
		}
		p := Prompt{ID: f[0], Lang: f[1], Text: strings.TrimSpace(f[2])}
		if p.Text == "" || len(p.Text) > maxText {
			return nil, fmt.Errorf("%w: line %d: the text is empty or longer than %d bytes", ErrTable, line, maxText)
		}
		key := p.Lang + "/" + p.ID
		if seen[key] {
			return nil, fmt.Errorf("%w: line %d: %s listed twice", ErrTable, line, key)
		}
		seen[key] = true
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !header {
		return nil, fmt.Errorf("%w: no header", ErrTable)
	}
	return out, nil
}

// For is the table's prompts in lang.
func For(all []Prompt, lang string) []Prompt {
	var out []Prompt
	for _, p := range all {
		if p.Lang == lang {
			out = append(out, p)
		}
	}
	return out
}
