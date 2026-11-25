//go:build !cgo

package audio

func newOpusDecoder(int, int) (Decoder, error) { return nil, ErrUnavailable }
