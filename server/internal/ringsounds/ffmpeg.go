package ringsounds

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	// what a song's upstream may be, read whole into ffmpeg
	maxSourceBytes = 64 << 20
	// what the ring sound may be: 10 min at 64 kbps is under 5 MiB
	maxOutputBytes = 8 << 20
	// a transcode of 10 min of audio takes seconds; past this it is given up
	transcodeTimeout = 3 * time.Minute
	musicBitrate     = "64k"
	fadeInSeconds    = 3
)

// Body is what a transcoder reads: a song's upstream, closed once read.
type Body = io.ReadCloser

var errTooLarge = errors.New("ringsounds: over the size limit")

// FFmpeg is the transcoder on the ffmpeg binary at path (CADENCE_FFMPEG, else ffmpeg on the PATH); nil when there is
// none. The song comes on its stdin, so no url is ever on a command line, and nothing ffmpeg says reaches a log.
func FFmpeg(getenv func(string) string) Transcoder {
	path := getenv("CADENCE_FFMPEG")
	if path == "" {
		found, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil
		}
		path = found
	} else if _, err := os.Stat(path); err != nil {
		return nil
	}
	return func(ctx context.Context, open func(ctx context.Context) (Body, error), maxSeconds int) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, transcodeTimeout)
		defer cancel()
		body, err := open(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = body.Close() }()
		filter := "loudnorm=I=-16:TP=-1.5:LRA=11,afade=t=in:st=0:d=" + strconv.Itoa(fadeInSeconds)
		//nolint:gosec // a fixed binary and fixed arguments; the song comes on stdin
		cmd := exec.CommandContext(ctx, path, "-nostdin", "-hide_banner", "-loglevel", "quiet",
			"-i", "pipe:0", "-t", strconv.Itoa(maxSeconds), "-vn", "-sn", "-dn", "-map_metadata", "-1",
			"-ac", "1", "-ar", "48000", "-af", filter, "-c:a", "libopus", "-b:a", musicBitrate, "-f", "ogg", "pipe:1")
		cmd.Stdin = &capped{r: body, left: maxSourceBytes}
		var out cappedBuffer
		out.left = maxOutputBytes
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		if out.over || out.Len() == 0 || !bytes.HasPrefix(out.Bytes(), []byte("OggS")) {
			return nil, errTooLarge
		}
		return out.Bytes(), nil
	}
}

// capped reads at most left bytes, then fails
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// cappedBuffer keeps at most left bytes and says when more came
type cappedBuffer struct {
	bytes.Buffer
	left int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.left {
		b.over = true
		return 0, errTooLarge
	}
	b.left -= len(p)
	return b.Buffer.Write(p)
}
