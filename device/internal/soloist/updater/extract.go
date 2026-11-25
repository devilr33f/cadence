package updater

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"runtime"
)

type limits struct {
	binary   int64 // the build
	inflated int64 // the build and everything else in the tarball, inflated
}

var defaultLimits = limits{binary: 128 << 20, inflated: 256 << 20}

const chunk = 64 << 10

// extract takes the build out of soloist's tarball while the body streams, so
// no tarball ever touches flash, into dest (mode 755). dest is gone after any
// failure. An error the body itself returned (a timeout, a reset) is the
// network's, http; a stream that is cut, not gzip or not tar is format; one
// past the limits is size; the disk's errors are io.
func extract(body io.Reader, dest string, lim limits) error {
	err := unpack(body, dest, lim)
	if err != nil {
		_ = os.Remove(dest)
	}
	return err
}

func unpack(body io.Reader, dest string, lim limits) error {
	w := &watched{r: body}
	zr, err := gzip.NewReader(w)
	if err != nil {
		return w.blame(FailFormat)
	}
	// one member, as v1 read it: bytes after it are never read
	zr.Multistream(false)
	// bounds what the decoder may make, so a hostile body cannot burn minutes of cpu
	inflated := &io.LimitedReader{R: zr, N: lim.inflated}
	err = take(tar.NewReader(inflated), dest, lim.binary)
	if err == nil {
		// the rest is read too, so that gzip's own length and checksum get checked
		if _, cerr := io.Copy(io.Discard, inflated); cerr != nil {
			err = FailFormat
		}
	}
	capped := inflated.N == 0
	switch {
	case errors.Is(err, FailFormat) && w.failed:
		return FailHTTP
	case (err == nil || errors.Is(err, FailFormat)) && capped:
		return FailSize
	}
	return err
}

// take writes the first regular entry named exactly soloist or ./soloist to
// dest, created only once the entry is found and sized; links, directories
// and every other name are skipped
func take(tr *tar.Reader, dest string, maxBinary int64) error {
	for {
		h, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return FailFormat
		case errors.Is(err, tar.ErrInsecurePath):
			// a non-local name is never ours
			continue
		case err != nil:
			return FailFormat
		}
		if h.Typeflag != tar.TypeReg || h.Name != "soloist" && h.Name != "./soloist" {
			continue
		}
		if h.Size > maxBinary {
			return FailSize
		}
		return write(tr, dest, h.Size)
	}
}

// whatever is at dest goes first: the exclusive create then never follows a
// link left there
func write(r io.Reader, dest string, size int64) error {
	_ = os.Remove(dest)
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755) //nolint:gosec // the updater's own soloist.next
	if err != nil {
		return FailIO
	}
	err = copyExact(f, r, size)
	// the umask may have taken bits from the mode, and the rename that follows must find the bytes on disk
	if err == nil && (f.Chmod(0o755) != nil && runtime.GOOS != "windows" || f.Sync() != nil) {
		err = FailIO
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = FailIO
	}
	return err
}

// a stream that ends early reads as the end of the entry: the count is what tells
func copyExact(w io.Writer, r io.Reader, size int64) error {
	buf := make([]byte, chunk)
	var written int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return FailIO
			}
			written += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return FailFormat
		}
	}
	if written != size {
		return FailFormat
	}
	return nil
}

// watched remembers that the body returned an error: past the decoder a cut
// stream and a broken socket look alike
type watched struct {
	r      io.Reader
	failed bool
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		w.failed = true
	}
	return n, err
}

func (w *watched) blame(f Failure) Failure {
	if w.failed {
		return FailHTTP
	}
	return f
}
