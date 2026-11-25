//go:build arm64 && !purego

#include "textflag.h"

// func gemmNEON(a, w *float32, k int, acc *[32]float32)
// acc[i][j] = sum over k of a[k][i] * w[k][j], i < 4 positions, j < 8 channels. per k: one load of the 4 positions,
// two of the 8 weights, and eight fmla by element (encoded as words: the assembler has no by-element form), the eight
// accumulators v0-v7 held across the loop. k is at least 1
TEXT ·gemmNEON(SB), NOSPLIT, $0-32
	MOVD a+0(FP), R0
	MOVD w+8(FP), R1
	MOVD k+16(FP), R2
	MOVD acc+24(FP), R3
	VEOR V0.B16, V0.B16, V0.B16
	VEOR V1.B16, V1.B16, V1.B16
	VEOR V2.B16, V2.B16, V2.B16
	VEOR V3.B16, V3.B16, V3.B16
	VEOR V4.B16, V4.B16, V4.B16
	VEOR V5.B16, V5.B16, V5.B16
	VEOR V6.B16, V6.B16, V6.B16
	VEOR V7.B16, V7.B16, V7.B16

loop:
	VLD1.P 16(R0), [V16.S4]
	VLD1.P 32(R1), [V17.S4, V18.S4]
	WORD $0x4f901220 // fmla v0.4s, v17.4s, v16.s[0]
	WORD $0x4f901241 // fmla v1.4s, v18.4s, v16.s[0]
	WORD $0x4fb01222 // fmla v2.4s, v17.4s, v16.s[1]
	WORD $0x4fb01243 // fmla v3.4s, v18.4s, v16.s[1]
	WORD $0x4f901a24 // fmla v4.4s, v17.4s, v16.s[2]
	WORD $0x4f901a45 // fmla v5.4s, v18.4s, v16.s[2]
	WORD $0x4fb01a26 // fmla v6.4s, v17.4s, v16.s[3]
	WORD $0x4fb01a47 // fmla v7.4s, v18.4s, v16.s[3]
	SUBS $1, R2, R2
	BNE  loop

	VST1.P [V0.S4, V1.S4, V2.S4, V3.S4], 64(R3)
	VST1   [V4.S4, V5.S4, V6.S4, V7.S4], (R3)
	RET
