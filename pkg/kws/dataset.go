package kws

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"slices"
	"strings"
)

// Enrollment is what training learns one word from, all 16 kHz mono: takes of the word, each one utterance with some
// silence around it; the room with nobody talking to the device (required); and optionally the owner talking
// normally, which teaches the head their own voice saying other things. Kept in memory for one training, never stored.
type Enrollment struct {
	Takes   [][]int16
	Ambient []int16
	Speech  []int16
}

// Dataset limits: a zip past them is refused before anything is decoded.
const (
	MaxDatasetBytes   = 256 << 20
	maxDatasetEntries = 256
	maxEntryBytes     = 64 << 20
	// MinTakes is the fewest takes training starts with; 6 to 15 work best
	MinTakes = 3
	MaxTakes = 100
)

// DatasetError is a refused dataset, by a code the owner can be shown (never file contents).
type DatasetError struct{ Code string }

func (e *DatasetError) Error() string { return "kws: dataset " + e.Code }

func datasetErr(code string) error { return &DatasetError{Code: code} }

// ReadDataset reads a zip of WAV files: every .wav in a directory named takes is a take; a .wav whose name starts
// with ambient (or one in a directory of that name) is the room; speech likewise is the owner's speech. Files of
// several ambient or speech parts are joined in name order. Any rate and channel count is converted (ReadWAV);
// anything else in the zip is ignored. Codes: too_large, not_zip, too_many_files, file_too_large, bad_wav (with no
// detail of which), no_takes, too_many_takes, no_ambient.
func ReadDataset(b []byte) (*Enrollment, error) {
	if len(b) > MaxDatasetBytes {
		return nil, datasetErr("too_large")
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, datasetErr("not_zip")
	}
	if len(zr.File) > maxDatasetEntries {
		return nil, datasetErr("too_many_files")
	}
	var total int64
	en := &Enrollment{}
	type part struct {
		name string
		pcm  []int16
	}
	var ambient, speech []part
	for _, f := range zr.File {
		name := strings.ToLower(path.Clean(strings.ReplaceAll(f.Name, "\\", "/")))
		base := path.Base(name)
		if f.FileInfo().IsDir() || !strings.HasSuffix(base, ".wav") || strings.HasPrefix(base, ".") ||
			strings.Contains(name, "__macosx") {
			continue
		}
		dir := path.Base(path.Dir(name))
		kind := ""
		switch {
		case dir == "takes":
			kind = "take"
		case dir == "ambient" || strings.HasPrefix(base, "ambient"):
			kind = "ambient"
		case dir == "speech" || strings.HasPrefix(base, "speech"):
			kind = "speech"
		default:
			continue
		}
		if f.UncompressedSize64 > maxEntryBytes {
			return nil, datasetErr("file_too_large")
		}
		raw, err := readEntry(f)
		if err != nil {
			return nil, err
		}
		if total += int64(len(raw)); total > MaxDatasetBytes {
			return nil, datasetErr("too_large")
		}
		pcm, err := ReadWAV(raw)
		if err != nil {
			return nil, datasetErr("bad_wav")
		}
		switch kind {
		case "take":
			en.Takes = append(en.Takes, pcm)
		case "ambient":
			ambient = append(ambient, part{name, pcm})
		case "speech":
			speech = append(speech, part{name, pcm})
		}
	}
	join := func(parts []part) []int16 {
		slices.SortFunc(parts, func(a, b part) int { return strings.Compare(a.name, b.name) })
		var out []int16
		for _, p := range parts {
			out = append(out, p.pcm...)
		}
		return out
	}
	en.Ambient, en.Speech = join(ambient), join(speech)
	switch {
	case len(en.Takes) == 0:
		return nil, datasetErr("no_takes")
	case len(en.Takes) > MaxTakes:
		return nil, datasetErr("too_many_takes")
	case len(en.Ambient) < SampleRate:
		return nil, datasetErr("no_ambient")
	}
	return en, nil
}

// readEntry reads one entry, never past maxEntryBytes whatever its header claims.
func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, datasetErr("not_zip")
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	switch {
	case err != nil && !errors.Is(err, io.EOF):
		return nil, datasetErr("not_zip")
	case len(raw) > maxEntryBytes:
		return nil, datasetErr("file_too_large")
	}
	return raw, nil
}
