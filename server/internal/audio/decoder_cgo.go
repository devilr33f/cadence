//go:build cgo

package audio

/*
#include <opus.h>
*/
import "C"

import (
	"runtime"
	"unsafe"
)

type opusDecoder struct {
	st       *C.OpusDecoder
	channels int
	// 120 ms per channel, the longest packet opus defines
	maxPerChannel int
	out           []int16
}

func newOpusDecoder(rate, channels int) (Decoder, error) {
	var code C.int
	st := C.opus_decoder_create(C.opus_int32(rate), C.int(channels), &code)
	if code != C.OPUS_OK || st == nil {
		return nil, ErrOptions
	}
	d := &opusDecoder{st: st, channels: channels, maxPerChannel: rate * 120 / 1000}
	d.out = make([]int16, d.maxPerChannel*channels)
	runtime.AddCleanup(d, func(st *C.OpusDecoder) { C.opus_decoder_destroy(st) }, st)
	return d, nil
}

func (d *opusDecoder) Decode(packet []byte) ([]int16, error) {
	if len(packet) == 0 {
		return nil, ErrDecode
	}
	n := C.opus_decode(d.st, (*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)),
		(*C.opus_int16)(unsafe.Pointer(&d.out[0])), C.int(d.maxPerChannel), 0)
	runtime.KeepAlive(d)
	if n < 0 {
		return nil, ErrDecode
	}
	return append([]int16(nil), d.out[:int(n)*d.channels]...), nil
}
