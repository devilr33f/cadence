package leds

// glyphs always describe HHMM, whatever the board's physical display is. bit i lights segment i (a..g).
type glyphs [4]uint8

var digitGlyphs = [10]uint8{0x3F, 0x06, 0x5B, 0x4F, 0x66, 0x6D, 0x7D, 0x07, 0x7F, 0x6F}

const (
	dash         = 0x40
	maxNumber    = 9999
	hour         = 3600
	maxCountdown = 99*hour + 59*60 + 59
)

func digitGlyph(v int) uint8 { return digitGlyphs[v%10] }

// "HHMM", leading zero kept
func timeGlyphs(h, m int) glyphs {
	return glyphs{digitGlyph(h / 10), digitGlyph(h), digitGlyph(m / 10), digitGlyph(m)}
}

// right-aligned, clamped to 0..9999
func numberGlyphs(v int) glyphs {
	v = min(max(v, 0), maxNumber)
	var g glyphs
	for i := len(g) - 1; i >= 0; i-- {
		g[i] = digitGlyph(v)
		v /= 10
		if v == 0 {
			break
		}
	}
	return g
}

func dashGlyphs() glyphs { return glyphs{dash, dash, dash, dash} }

// m:ss under an hour, h:mm from then on, minutes or hours right-aligned; the colon between them is the caller's.
// held at 99:59
func countdownGlyphs(seconds int) glyphs {
	seconds = min(max(seconds, 0), maxCountdown)
	major, minor := seconds/60, seconds%60
	if seconds >= hour {
		major, minor = seconds/hour, seconds%hour/60
	}
	var first uint8
	if major >= 10 {
		first = digitGlyph(major / 10)
	}
	return glyphs{first, digitGlyph(major), digitGlyph(minor / 10), digitGlyph(minor)}
}

// show lights the segments the glyphs spell at value and blanks the rest of the digits; the colon is left alone
func (g glyphs) show(f *Frame, value uint8) {
	if len(f.Digits) != 4 {
		return
	}
	for d := range f.Digits {
		for s := 0; s < 7; s++ {
			f.Digits[d][s] = 0
			if g[d]>>s&1 != 0 {
				f.Digits[d][s] = value
			}
		}
	}
}
