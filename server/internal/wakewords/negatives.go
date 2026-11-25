package wakewords

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/kws"
)

// openWakeWord's precomputed features (CC BY-NC-SA 4.0, huggingface.co/datasets/davidscripka/openwakeword_features):
// the ACAV100M windows (2000 hours of speech, music and noise from real video, 16 embeddings each, float16) and the
// false-positive validation stream (conversation, parties and music, float32). Only byte ranges are fetched: a sample
// of each, about 25 MiB in all, written to the negatives file in pkg/kws's format.
const (
	featuresBase   = "https://huggingface.co/datasets/davidscripka/openwakeword_features/resolve/main/"
	acavFile       = "openwakeword_features_ACAV100M_2000_hrs_16bit.npy"
	validationFile = "validation_set_features.npy"
	// windows: chunks of consecutive rows at random offsets
	windowChunks   = 60
	windowsInChunk = 50
	// streams: runs of consecutive embeddings (13 min each)
	streamCount  = 4
	streamLen    = 10000
	fetchTimeout = 2 * time.Minute
	// the license the fetched file carries in its source line
	negativesLicense = "CC BY-NC-SA 4.0"
)

// Fetcher reads a byte range of one of the features' files.
type Fetcher interface {
	Range(ctx context.Context, file string, offset, length int64) ([]byte, error)
}

type huggingFace struct{}

func (huggingFace) Range(ctx context.Context, file string, offset, length int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, featuresBase+file, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(offset+length-1, 10))
	req.Header.Set("User-Agent", "cadence-server")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("fetch")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("status_%d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil || int64(len(b)) != length {
		return nil, errors.New("short")
	}
	return b, nil
}

// WakeWordNegatives is the negatives file's state.
type WakeWordNegatives struct {
	// whether CADENCE_WAKEWORD_NEGATIVES names a file
	Configured bool  `json:"configured"`
	Present    bool  `json:"present"`
	Bytes      int64 `json:"bytes"`
	Windows    int   `json:"windows"`
	Streams    int   `json:"streams"`
	// where they came from, with their license
	Source   string                     `json:"source,omitempty"`
	Fetching *WakeWordNegativesProgress `json:"fetching,omitempty"`
}

type WakeWordNegativesProgress struct {
	Fraction float64 `json:"fraction" minimum:"0" maximum:"1"`
	// a failed fetch's code, until the next
	Code string `json:"code,omitempty"`
	Done bool   `json:"done"`
}

func (s *Service) Negatives() WakeWordNegatives {
	v := WakeWordNegatives{Configured: s.d.Negatives != ""}
	s.mu.Lock()
	if s.fetching != nil {
		p := *s.fetching
		v.Fetching = &p
	}
	s.mu.Unlock()
	if !v.Configured {
		return v
	}
	if fi, err := os.Stat(s.d.Negatives); err == nil {
		v.Bytes = fi.Size()
		if n, err := s.negatives(); err == nil {
			v.Present, v.Windows, v.Streams, v.Source = true, len(n.Windows), len(n.Streams), n.Source
		}
	}
	return v
}

// FetchNegatives starts fetching the negatives into their file, unless a fetch runs.
func (s *Service) FetchNegatives() error {
	if s.d.Negatives == "" {
		return ErrNoPath
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetching != nil && !s.fetching.Done {
		return ErrFetching
	}
	s.fetching = &WakeWordNegativesProgress{}
	go s.fetch(context.Background())
	return nil
}

func (s *Service) fetch(ctx context.Context) {
	report := func(p WakeWordNegativesProgress) {
		s.mu.Lock()
		s.fetching = &p
		s.mu.Unlock()
		s.publish(EventNegatives, map[string]any{"fraction": p.Fraction, "code": p.Code, "done": p.Done})
	}
	began := s.d.Now()
	n, err := Fetch(ctx, s.d.Fetch, uint64(began.UnixNano()), func(f float64) { report(WakeWordNegativesProgress{Fraction: f}) })
	if err == nil {
		err = writeNegatives(s.d.Negatives, n)
	}
	if err != nil {
		code := "fetch_failed"
		if errors.Is(err, errWrite) {
			code = "write_failed"
		}
		s.d.Log.Warn("wakeword_negatives_failed", "code", code)
		report(WakeWordNegativesProgress{Code: code, Done: true})
		return
	}
	s.d.Log.Info("wakeword_negatives_fetched", "windows", len(n.Windows), "streams", len(n.Streams),
		"ms", s.d.Now().Sub(began).Milliseconds())
	report(WakeWordNegativesProgress{Fraction: 1, Done: true})
}

var errWrite = errors.New("write")

func writeNegatives(path string, n *kws.Negatives) error {
	var buf bytes.Buffer
	if err := kws.WriteNegatives(&buf, n); err != nil {
		return errWrite
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errWrite
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return errWrite
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errWrite
	}
	return nil
}

// Fetch samples the features through f: windowChunks runs of windowsInChunk ACAV100M windows and streamCount runs of
// streamLen validation embeddings, at random offsets of seed.
func Fetch(ctx context.Context, f Fetcher, seed uint64, progress func(float64)) (*kws.Negatives, error) {
	r := rand.New(rand.NewPCG(seed, 0x6e6567)) //nolint:gosec // which rows to sample, no secret
	total := float64(windowChunks + streamCount + 2)
	done := 0.0
	step := func() {
		done++
		progress(done / total)
	}
	acav, err := readHeader(ctx, f, acavFile)
	if err != nil {
		return nil, err
	}
	step()
	if len(acav.shape) != 3 || acav.shape[1] != kws.Context || acav.shape[2] != kws.EmbeddingDim || acav.descr != "<f2" {
		return nil, errors.New("acav shape")
	}
	n := &kws.Negatives{Source: "openWakeWord features (" + negativesLicense + "): " + featuresBase + acavFile +
		", " + featuresBase + validationFile}
	row := int64(kws.Context * kws.EmbeddingDim * 2)
	chunks := acav.shape[0] / windowsInChunk
	for _, c := range sample(r, chunks, windowChunks) {
		b, err := f.Range(ctx, acavFile, acav.offset+int64(c)*windowsInChunk*row, windowsInChunk*row)
		if err != nil {
			return nil, err
		}
		for w := range windowsInChunk {
			win := make([]float32, kws.Context*kws.EmbeddingDim)
			for i := range win {
				win[i] = kws.FromHalf(binary.LittleEndian.Uint16(b[int64(w)*row+int64(2*i):]))
			}
			n.Windows = append(n.Windows, win)
		}
		step()
	}
	val, err := readHeader(ctx, f, validationFile)
	if err != nil {
		return nil, err
	}
	step()
	if len(val.shape) != 2 || val.shape[1] != kws.EmbeddingDim || val.descr != "<f4" {
		return nil, errors.New("validation shape")
	}
	vrow := int64(kws.EmbeddingDim * 4)
	for _, c := range sample(r, val.shape[0]/streamLen, streamCount) {
		b, err := f.Range(ctx, validationFile, val.offset+int64(c)*streamLen*vrow, streamLen*vrow)
		if err != nil {
			return nil, err
		}
		s := make([]float32, streamLen*kws.EmbeddingDim)
		for i := range s {
			s[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
		n.Streams = append(n.Streams, s)
		step()
	}
	return n, nil
}

// sample is k distinct numbers below n, sorted (all of them when n <= k).
func sample(r *rand.Rand, n, k int) []int {
	if n <= 0 {
		return nil
	}
	if n <= k {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	picked := map[int]bool{}
	for len(picked) < k {
		picked[r.IntN(n)] = true
	}
	out := make([]int, 0, k)
	for i := range n {
		if picked[i] {
			out = append(out, i)
		}
		if len(out) == k {
			break
		}
	}
	return out
}

type npy struct {
	offset int64
	descr  string
	shape  []int
}

var (
	npyDescr = regexp.MustCompile(`'descr':\s*'([^']+)'`)
	npyShape = regexp.MustCompile(`'shape':\s*\(([^)]*)\)`)
	npyOrder = regexp.MustCompile(`'fortran_order':\s*False`)
)

// readHeader reads a .npy file's header: its data's offset, dtype and shape (C order only).
func readHeader(ctx context.Context, f Fetcher, file string) (npy, error) {
	b, err := f.Range(ctx, file, 0, 1024)
	if err != nil {
		return npy{}, err
	}
	if len(b) < 12 || string(b[:6]) != "\x93NUMPY" {
		return npy{}, errors.New("npy magic")
	}
	var hl, start int
	switch b[6] {
	case 1:
		hl, start = int(binary.LittleEndian.Uint16(b[8:])), 10
	default:
		hl, start = int(binary.LittleEndian.Uint32(b[8:])), 12
	}
	if start+hl > len(b) {
		return npy{}, errors.New("npy header")
	}
	h := string(b[start : start+hl])
	d, s := npyDescr.FindStringSubmatch(h), npyShape.FindStringSubmatch(h)
	if d == nil || s == nil || !npyOrder.MatchString(h) {
		return npy{}, errors.New("npy header")
	}
	out := npy{offset: int64(start + hl), descr: d[1]}
	for _, part := range strings.Split(s[1], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 {
			return npy{}, errors.New("npy shape")
		}
		out.shape = append(out.shape, v)
	}
	return out, nil
}
