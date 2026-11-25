package providers

import (
	"context"
	"errors"
)

// Embedder turns text into vectors. Embed takes at most MaxEmbedInputs texts,
// cuts each to MaxEmbedInputRunes and returns one vector per text, all of one
// length; a length that changes between calls is an error. Failures carry
// CodeEmbedUnavailable, CodeEmbedFailed or CodeEmbedTimeout, never upstream
// text. Model names the model, which callers store with each vector.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Model() string
}

const (
	MaxEmbedInputs     = 32
	MaxEmbedInputRunes = 8000
)

// PrepareEmbedInputs checks the batch size and cuts each text on a rune boundary.
func PrepareEmbedInputs(inputs []string) ([]string, error) {
	if len(inputs) > MaxEmbedInputs {
		return nil, errors.New(CodeEmbedFailed)
	}
	out := make([]string, len(inputs))
	for i, s := range inputs {
		out[i] = s
		if len(s) > MaxEmbedInputRunes {
			if r := []rune(s); len(r) > MaxEmbedInputRunes {
				out[i] = string(r[:MaxEmbedInputRunes])
			}
		}
	}
	return out, nil
}
