//go:build !cgo

package audio

func newOpus(EncoderOptions) (Encoder, error) { return nil, ErrUnavailable }
