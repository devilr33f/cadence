package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"sync"

	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the server's own bound on an uploaded cover
const maxArtworkBytes = 1 << 20

var errArtwork = errors.New("media: cover unusable")

// Uploader sends a cover's bytes to the server under their hex SHA-256 (PUT /v2/artwork/{sha256}).
type Uploader func(ctx context.Context, sha256Hex, contentType string, body []byte) error

// artwork passes on the covers a source handed audiod as bytes (airplay): audiod writes one at path and names it by
// an id; the first state naming a new id goes out without it, the cover is read and hashed on a goroutine of its own,
// handed to the local view (the device's own screen) and uploaded, and the latest state goes out again with its hash
// once the server has it. a cover that fails is not tried again until the id changes. logs carry sizes and codes only
type artwork struct {
	path string
	// nil: the cover is read for the local view alone
	upload Uploader

	mu sync.Mutex
	// the id uploaded and its hash; the id under way or failed
	doneID, doneSHA string
	triedID         string
	// the latest cover read, for the local view, and its id
	readID string
	read   *Cover
}

// Putter is the device's client of the server's https routes.
type Putter interface {
	Put(ctx context.Context, path, contentType string, body []byte) (status int, err error)
}

// uploadError's text is a fixed code: send, or the status the server answered
type uploadError struct{ code string }

func (e uploadError) Error() string { return e.code }

// ServerUploader uploads through c to the server's PUT /v2/artwork/{sha256}; nil without a client.
func ServerUploader(c Putter) Uploader {
	// a nil *otlp.Client in the interface is none as well
	if v := reflect.ValueOf(c); c == nil || v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return func(ctx context.Context, hash, contentType string, body []byte) error {
		status, err := c.Put(ctx, "/v2/artwork/"+hash, contentType, body)
		switch {
		case err != nil:
			return uploadError{"send"}
		case status != http.StatusNoContent && status != http.StatusOK:
			return uploadError{"status_" + strconv.Itoa(status)}
		}
		return nil
	}
}

// UseArtwork has covers audiod writes at path uploaded through upload, and handed to the local view (UseLocal); with
// neither an upload nor a local view nothing is read. Call it before audiod connects.
func (m *Media) UseArtwork(path string, upload Uploader) {
	if upload != nil || path != "" {
		m.art = &artwork{path: path, upload: upload}
	}
}

// withArtwork fills in the hash of the state's cover once uploaded, and starts its upload when it is new
func (m *Media) withArtwork(s *cadencev2.MediaSessionState, id string) {
	a := m.art
	if a == nil || id == "" || s.GetMetadata() == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == a.doneID {
		s.Metadata.ArtworkSha256 = a.doneSHA
		return
	}
	if id == a.triedID {
		return
	}
	// callers hold stateMu, which guards local
	if a.upload == nil && m.local == nil {
		return
	}
	a.triedID = id
	go m.uploadArtwork(id)
}

// cover is the cover read for id, if it was
func (m *Media) cover(id string) *Cover {
	a := m.art
	if a == nil || id == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readID != id {
		return nil
	}
	return a.read
}

func (m *Media) uploadArtwork(id string) {
	a := m.art
	body, typ, err := readCover(a.path)
	if err != nil {
		m.log.Warn("artwork_unusable")
		return
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	a.mu.Lock()
	a.readID, a.read = id, &Cover{SHA256: hash, Type: typ, Data: body}
	a.mu.Unlock()
	// the screen shows it at once, whatever the server makes of it
	m.stateMu.Lock()
	if m.latest != nil && m.latestArt == id {
		m.notifyLocal()
	}
	m.stateMu.Unlock()
	if a.upload == nil {
		return
	}
	if err := a.upload(context.Background(), hash, typ, body); err != nil {
		m.log.Warn("artwork_upload_failed", "code", err.Error())
		return
	}
	m.log.Info("artwork_uploaded", "bytes", len(body))
	a.mu.Lock()
	a.doneID, a.doneSHA = id, hash
	a.mu.Unlock()
	// the latest state, if it still shows this cover, goes out again with its hash
	// under the lock, so no newer state is offered between the read and this one
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.latest != nil && m.latestArt == id {
		s := proto.Clone(m.latest).(*cadencev2.MediaSessionState)
		s.Metadata.ArtworkSha256 = hash
		m.latest = s
		offer(m.sessions, s)
		m.notifyLocal()
	}
}

func readCover(path string) ([]byte, string, error) {
	f, err := os.Open(path) //nolint:gosec // audiod's own scratch, named by the board
	if err != nil {
		return nil, "", errArtwork
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, maxArtworkBytes+1))
	if err != nil || len(body) > maxArtworkBytes {
		return nil, "", errArtwork
	}
	if typ := coverType(body); typ != "" {
		return body, typ, nil
	}
	return nil, "", errArtwork
}

// coverType sniffs the two formats a screen shows: jpeg and png; "" for another
func coverType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	}
	return ""
}
