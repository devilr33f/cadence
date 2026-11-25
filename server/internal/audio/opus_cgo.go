//go:build cgo

package audio

/*
#cgo pkg-config: opus
#cgo LDFLAGS: -Wl,-Bstatic -lopus -Wl,-Bdynamic -lm
#include <opus.h>

// the ctl requests are variadic macros, which cgo cannot call
static int cadence_opus_set_bitrate(OpusEncoder *st, opus_int32 v) {
	return opus_encoder_ctl(st, OPUS_SET_BITRATE(v));
}

static int cadence_opus_set_complexity(OpusEncoder *st, opus_int32 v) {
	return opus_encoder_ctl(st, OPUS_SET_COMPLEXITY(v));
}
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// libopus's recommended upper bound for one packet
const maxPacketBytes = 4000

var errEncode = errors.New("audio: opus encode failed")

type opusEncoder struct {
	st         *C.OpusEncoder
	perChannel int
	samples    int
	out        []byte
}

func newOpus(o EncoderOptions) (Encoder, error) {
	app := C.int(C.OPUS_APPLICATION_AUDIO)
	if o.Application == AppVoIP {
		app = C.OPUS_APPLICATION_VOIP
	}
	var code C.int
	st := C.opus_encoder_create(C.opus_int32(o.SampleRate), C.int(o.Channels), app, &code)
	if code != C.OPUS_OK || st == nil {
		return nil, ErrOptions
	}
	if C.cadence_opus_set_bitrate(st, C.opus_int32(o.Bitrate)) != C.OPUS_OK ||
		C.cadence_opus_set_complexity(st, C.opus_int32(o.Complexity)) != C.OPUS_OK {
		C.opus_encoder_destroy(st)
		return nil, ErrOptions
	}
	e := &opusEncoder{
		st: st, perChannel: o.SampleRate / 50, samples: o.SampleRate / 50 * o.Channels,
		out: make([]byte, maxPacketBytes),
	}
	runtime.AddCleanup(e, func(st *C.OpusEncoder) { C.opus_encoder_destroy(st) }, st)
	return e, nil
}

func (e *opusEncoder) FrameSamples() int { return e.samples }

func (e *opusEncoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != e.samples {
		return nil, ErrFrameSize
	}
	n := C.opus_encode(e.st, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(e.perChannel),
		(*C.uchar)(unsafe.Pointer(&e.out[0])), C.opus_int32(len(e.out)))
	runtime.KeepAlive(e)
	if n < 0 {
		return nil, errEncode
	}
	return append([]byte(nil), e.out[:n]...), nil
}

// packetSamples is libopus's own count of samples per channel in packet p.
func packetSamples(p []byte, rate int) int {
	return int(C.opus_packet_get_nb_samples((*C.uchar)(unsafe.Pointer(&p[0])), C.opus_int32(len(p)), C.opus_int32(rate)))
}
