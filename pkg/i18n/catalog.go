package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
)

// KeyPattern is a catalog key: dotted lower-case segments, player.osd.voiceover.
var KeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Catalog is one area's messages (i18n/<area>/<lang>.json) in every language, xx made from en.
type Catalog struct {
	area string
	raw  map[Lang]map[string]string
	msgs map[Lang]map[string]Message
}

// Text is a message to render later, in the language of whoever shows it.
type Text struct {
	Key  string
	Args Args
}

// Load reads <area>/en.json and <area>/ru.json from fsys: each one flat json object of key to message, every
// message parsed. It does not check completeness: Check does.
func Load(fsys fs.FS, area string) (*Catalog, error) {
	c := &Catalog{area: area, raw: map[Lang]map[string]string{}, msgs: map[Lang]map[string]Message{}}
	for _, l := range Catalogued {
		file := path.Join(area, string(l)+".json")
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, fmt.Errorf("i18n: %w", err)
		}
		if err := c.add(l, file, data); err != nil {
			return nil, err
		}
	}
	pseudo := map[string]Message{}
	for k, m := range c.msgs[En] {
		pseudo[k] = Pseudolocalize(m)
	}
	c.msgs[Pseudo] = pseudo
	return c, nil
}

func (c *Catalog) add(l Lang, file string, data []byte) error {
	raw, err := decodeFlat(data)
	if err != nil {
		return fmt.Errorf("i18n: %s: %w", file, err)
	}
	msgs := make(map[string]Message, len(raw))
	for k, v := range raw {
		m, err := Parse(v)
		if err != nil {
			return fmt.Errorf("i18n: %s: %s: %w", file, k, err)
		}
		msgs[k] = m
	}
	c.raw[l], c.msgs[l] = raw, msgs
	return nil
}

// decodeFlat reads one json object of string values, refusing nesting and repeated keys
func decodeFlat(data []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a json object")
	}
	out := map[string]string{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		t, err = dec.Token()
		if err != nil {
			return nil, err
		}
		v, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("%s: not a string", k)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s: repeated", k)
		}
		out[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the object")
	}
	return out, nil
}

// Area is the catalog's area: screen or server.
func (c *Catalog) Area() string { return c.area }

// Keys lists en's keys, sorted.
func (c *Catalog) Keys() []string { return slices.Sorted(maps.Keys(c.raw[En])) }

// Source is a key's message as written in a catalogued language.
func (c *Catalog) Source(l Lang, key string) (string, bool) {
	s, ok := c.raw[l][key]
	return s, ok
}

// ArgsOf lists the arguments en's message takes.
func (c *Catalog) ArgsOf(key string) map[string]ArgType {
	m, ok := c.msgs[En][key]
	if !ok {
		return nil
	}
	args, _ := m.Args()
	return args
}

// Render is a key's message in a language with its arguments: a key the language lacks falls back to en, and one
// en lacks renders as the key itself, so a screen never shows nothing.
func (c *Catalog) Render(l Lang, key string, args Args) string {
	m, ok := c.msgs[l][key]
	if !ok {
		l = En
		if m, ok = c.msgs[En][key]; !ok {
			return key
		}
	}
	s, _ := m.Format(l, args)
	return s
}

// RenderText renders a Text.
func (c *Catalog) RenderText(l Lang, t Text) string { return c.Render(l, t.Key, t.Args) }

// Check is the completeness check: every key well-formed and in every catalogued language, no language with a key
// en lacks, the same arguments of the same types everywhere, and every plural with exactly its language's categories.
func (c *Catalog) Check() []error {
	var errs []error
	for _, k := range c.Keys() {
		if !KeyPattern.MatchString(k) {
			errs = append(errs, fmt.Errorf("%s: bad key %q", c.area, k))
		}
		want, err := c.msgs[En][k].Args()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/en: %s: %w", c.area, k, err))
		}
		for _, l := range Catalogued {
			m, ok := c.msgs[l][k]
			if !ok {
				errs = append(errs, fmt.Errorf("%s/%s: missing %s", c.area, l, k))
				continue
			}
			if l != En {
				got, err := m.Args()
				if err != nil {
					errs = append(errs, fmt.Errorf("%s/%s: %s: %w", c.area, l, k, err))
				} else if !maps.Equal(got, want) {
					errs = append(errs, fmt.Errorf("%s/%s: %s: arguments %v, en has %v", c.area, l, k, got, want))
				}
			}
			for _, cats := range m.PluralKeys() {
				if !slices.Equal(sortedCats(cats), sortedCats(Categories(l))) {
					errs = append(errs, fmt.Errorf("%s/%s: %s: plural cases %v, %s needs %v", c.area, l, k, cats, l,
						Categories(l)))
				}
			}
		}
	}
	for _, l := range Catalogued {
		for k := range c.raw[l] {
			if _, ok := c.raw[En][k]; !ok {
				errs = append(errs, fmt.Errorf("%s/%s: %s is not in en", c.area, l, k))
			}
		}
	}
	return errs
}

func sortedCats(cs []PluralCategory) []PluralCategory {
	out := slices.Clone(cs)
	slices.Sort(out)
	return out
}
