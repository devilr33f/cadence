package fake

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"strings"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	fakeEmbedDim   = 64
	fakeEmbedModel = "fake-embed"
)

var embedModule = providers.Module{
	ID:           "fake-embed",
	Kind:         providers.KindEmbed,
	Probe:        probe,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	New:          func(json.RawMessage, map[string]string) (any, error) { return embedder{}, nil },
}

type embedder struct{}

func (embedder) Model() string { return fakeEmbedModel }

func (embedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	in, err := providers.PrepareEmbedInputs(inputs)
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(in))
	for i, s := range in {
		out[i] = embedText(s)
	}
	return out, nil
}

// embedText counts hashed lowercase word tokens per bucket and L2-normalises,
// so texts sharing words have a higher cosine; text with no words is the unit
// vector on bucket 0 so it stays a valid vector.
func embedText(s string) []float32 {
	v := make([]float64, fakeEmbedDim)
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		v[h.Sum32()%fakeEmbedDim]++
	}
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n == 0 {
		v[0], n = 1, 1
	}
	n = math.Sqrt(n)
	out := make([]float32, fakeEmbedDim)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
