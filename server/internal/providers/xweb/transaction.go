package xweb

// the x-client-transaction-id header X's web app signs each api call with, ported from the MIT-licensed
// XClientTransaction (github.com/iSarabjitDhiman/XClientTransaction, copyright 2025 Sarabjit Dhiman): a key from the
// home page's twitter-site-verification meta tag, indices read from the ondemand.s script, and a frame of the page's
// loading animation pick an "animation key"; each call hashes its method, path and time with it

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	txKeyword      = "obfiowerehiring"
	txExtraByte    = 3
	txEpochSeconds = 1682924400
	txTotalTime    = 4096.0
)

var (
	errNoSigner = errors.New("xweb: the page lacks what signing needs")

	verificationRe  = regexp.MustCompile(`<meta[^>]+name="twitter-site-verification"[^>]+content="([^"]+)"`)
	verificationRe2 = regexp.MustCompile(`<meta[^>]+content="([^"]+)"[^>]+name="twitter-site-verification"`)
	indicesRe       = regexp.MustCompile(`\(\w\[(\d{1,2})\],\s*16\)`)
	pathDRe         = regexp.MustCompile(`<path[^>]*\sd="([^"]+)"`)
	nonDigitsRe     = regexp.MustCompile(`[^\d]+`)
)

// signer makes transaction ids for one home page and ondemand.s script
type signer struct {
	key          []byte
	animationKey string
}

func newSigner(home, ondemand string) (*signer, error) {
	key, err := verificationKey(home)
	if err != nil {
		return nil, err
	}
	m := indicesRe.FindAllStringSubmatch(ondemand, -1)
	if len(m) < 2 {
		return nil, errNoSigner
	}
	idx := make([]int, len(m))
	for i, s := range m {
		idx[i], _ = strconv.Atoi(s[1])
		if idx[i] >= len(key) {
			return nil, errNoSigner
		}
	}
	if len(key) < 6 {
		return nil, errNoSigner
	}
	rows, err := frameRows(home, int(key[5])%4)
	if err != nil {
		return nil, err
	}
	row := int(key[idx[0]]) % 16
	if row >= len(rows) || len(rows[row]) < 11 {
		return nil, errNoSigner
	}
	frameTime := 1
	for _, i := range idx[1:] {
		frameTime *= int(key[i]) % 16
	}
	frameTime = int(jsRound(float64(frameTime)/10)) * 10
	return &signer{key: key, animationKey: animate(rows[row], float64(frameTime)/txTotalTime)}, nil
}

func verificationKey(home string) ([]byte, error) {
	m := verificationRe.FindStringSubmatch(home)
	if m == nil {
		m = verificationRe2.FindStringSubmatch(home)
	}
	if m == nil {
		return nil, errNoSigner
	}
	key, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return nil, errNoSigner
	}
	return key, nil
}

// frameRows reads the loading animation's frame n (the svg loading-x-anim-<n>): its second path's d, past its first
// 9 characters, cut at each C into rows of numbers
func frameRows(home string, n int) ([][]int, error) {
	at := strings.Index(home, fmt.Sprintf(`id="loading-x-anim-%d"`, n))
	if at < 0 {
		return nil, errNoSigner
	}
	svg := home[at:]
	if end := strings.Index(svg, "</svg>"); end >= 0 {
		svg = svg[:end]
	}
	paths := pathDRe.FindAllStringSubmatch(svg, 2)
	if len(paths) < 2 || len(paths[1][1]) <= 9 {
		return nil, errNoSigner
	}
	var rows [][]int
	for part := range strings.SplitSeq(paths[1][1][9:], "C") {
		var row []int
		for f := range strings.FieldsSeq(nonDigitsRe.ReplaceAllString(part, " ")) {
			v, err := strconv.Atoi(f)
			if err != nil {
				return nil, errNoSigner
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func solve(v, lo, hi float64, floor bool) float64 {
	r := v*(hi-lo)/255 + lo
	if floor {
		return math.Floor(r)
	}
	return pyRound2(r)
}

func animate(f []int, t float64) string {
	from := []float64{float64(f[0]), float64(f[1]), float64(f[2]), 1}
	to := []float64{float64(f[3]), float64(f[4]), float64(f[5]), 1}
	toRotation := solve(float64(f[6]), 60, 360, true)
	var curves []float64
	for i, v := range f[7:] {
		lo := 0.0
		if i%2 == 1 {
			lo = -1
		}
		curves = append(curves, solve(float64(v), lo, 1, false))
	}
	val := cubicValue(curves, t)
	var b strings.Builder
	for i := range 3 {
		c := min(255, max(0, from[i]*(1-val)+to[i]*val))
		b.WriteString(strconv.FormatInt(int64(math.RoundToEven(c)), 16))
	}
	rad := (toRotation * val) * math.Pi / 180
	for _, v := range []float64{math.Cos(rad), -math.Sin(rad), math.Sin(rad), math.Cos(rad)} {
		r := math.Abs(pyRound2(v))
		h := floatToHex(r)
		switch {
		case strings.HasPrefix(h, "."):
			b.WriteString(strings.ToLower("0" + h))
		case h == "":
			b.WriteString("0")
		default:
			b.WriteString(h)
		}
	}
	b.WriteString("00")
	return strings.NewReplacer(".", "", "-", "").Replace(b.String())
}

func cubicValue(c []float64, t float64) float64 {
	if len(c) < 4 {
		return t
	}
	if t <= 0 {
		g := 0.0
		if c[0] > 0 {
			g = c[1] / c[0]
		} else if c[1] == 0 && c[2] > 0 {
			g = c[3] / c[2]
		}
		return g * t
	}
	if t >= 1 {
		g := 0.0
		if c[2] < 1 {
			g = (c[3] - 1) / (c[2] - 1)
		} else if c[2] == 1 && c[0] < 1 {
			g = (c[1] - 1) / (c[0] - 1)
		}
		return 1 + g*(t-1)
	}
	calc := func(a, b, m float64) float64 { return 3*a*(1-m)*(1-m)*m + 3*b*(1-m)*m*m + m*m*m }
	start, end, mid := 0.0, 1.0, 0.0
	for start < end {
		mid = (start + end) / 2
		x := calc(c[0], c[2], mid)
		if math.Abs(t-x) < 0.00001 {
			return calc(c[1], c[3], mid)
		}
		if x < t {
			start = mid
		} else {
			end = mid
		}
	}
	return calc(c[1], c[3], mid)
}

// floatToHex writes x in base 16 as the reference does: upper-case digits, "" for 0, a leading "." below 1
func floatToHex(x float64) string {
	var out []byte
	q := int64(x)
	frac := x - float64(q)
	// a base-16 digit, 0 to 15
	digit := func(d int64) byte { return "0123456789ABCDEF"[d&15] }
	for q > 0 {
		q = int64(x / 16)
		r := int64(x - float64(q)*16)
		out = append([]byte{digit(r)}, out...)
		x = float64(q)
	}
	if frac == 0 {
		return string(out)
	}
	out = append(out, '.')
	for frac > 0 {
		frac *= 16
		i := int64(frac)
		frac -= float64(i)
		out = append(out, digit(i))
	}
	return string(out)
}

// jsRound is the reference's Math.round: half away from zero for positives
func jsRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		f = math.Ceil(x)
	}
	return math.Copysign(f, x)
}

// pyRound2 is python's round(x, 2)
// pyRound2 is python's round(x, 2): the exact binary value rounded, ties to even
func pyRound2(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 2, 64), 64)
	return v
}

// id signs one call: random is the xor byte, seconds the time since x's epoch
func (s *signer) id(method, path string, seconds int64, random byte) string {
	// the low four bytes, little-endian, as the reference writes the time
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], uint32(seconds&0xffffffff)) //nolint:gosec // masked to 32 bits

	h := sha256.Sum256([]byte(fmt.Sprintf("%s!%s!%d%s%s", method, path, seconds, txKeyword, s.animationKey)))
	plain := append(append(append(append([]byte{}, s.key...), t[:]...), h[:16]...), txExtraByte)
	out := make([]byte, 0, len(plain)+1)
	out = append(out, random)
	for _, b := range plain {
		out = append(out, b^random)
	}
	return strings.TrimRight(base64.StdEncoding.EncodeToString(out), "=")
}
