package deviceops

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/pkg/opsplan"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// ArtifactView is an artifact as the dashboard sees it (never the blob).
type ArtifactView struct {
	ID        string              `json:"id"`
	Board     string              `json:"board"`
	Kind      string              `json:"kind"`
	Version   string              `json:"version"`
	Label     string              `json:"label"`
	SHA256    string              `json:"sha256"`
	Size      int64               `json:"size"`
	Files     []opsplan.FileEntry `json:"files"`
	CreatedAt time.Time           `json:"createdAt"`
}

// Upload stores an artifact: a device tarball (its MANIFEST names the version and the files, with
// each tar entry's size and mode) or an apk (one file, the version given by the uploader). The
// same board and version replaces what was there.
func (s *Service) Upload(ctx context.Context, board, kind, label, version string, data []byte) (ArtifactView, error) {
	if len(data) == 0 || int64(len(data)) > maxArtifact {
		return ArtifactView{}, ErrBadArtifact
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	var files []opsplan.FileEntry
	switch kind {
	case "tar":
		m, fs, err := parseTar(data)
		if err != nil {
			return ArtifactView{}, err
		}
		version, files = m.Version(), fs
	case "apk", "deb":
		if version == "" {
			return ArtifactView{}, fmt.Errorf("%w: an apk or deb needs its version named", ErrBadArtifact)
		}
		files = []opsplan.FileEntry{{Path: wholeName(kind, version), SHA256: sha, Size: int64(len(data)), Mode: 0o644}}
	default:
		return ArtifactView{}, fmt.Errorf("%w: kind %q", ErrBadArtifact, kind)
	}
	fb, err := json.Marshal(files)
	if err != nil {
		return ArtifactView{}, err
	}
	id := newID()
	if _, err := s.q.PutDeviceArtifact(ctx, db.PutDeviceArtifactParams{
		ID: id, Board: board, Kind: kind, Version: version, Label: label, Sha256: sha,
		Size: int64(len(data)), Data: data, Files: fb, Now: ts(s.now()),
	}); err != nil {
		return ArtifactView{}, err
	}
	s.gc(ctx, board)
	s.log.Info("ops_artifact_stored", "board", board, "kind", kind, "version", version, "size", len(data))
	return ArtifactView{ID: id.String(), Board: board, Kind: kind, Version: version, Label: label,
		SHA256: sha, Size: int64(len(data)), Files: files, CreatedAt: s.now()}, nil
}

// wholeName is the one file an apk or deb artifact is.
func wholeName(kind, version string) string {
	if kind == "deb" {
		return "cadence-" + version + "-rpi-arm64.deb"
	}
	return "cadence-" + version + ".apk"
}

// parseTar reads a device tarball's MANIFEST (its first entry) and each listed file's size and
// mode, checking every file the manifest names is there with the hash it claims.
func parseTar(data []byte) (opsplan.Manifest, []opsplan.FileEntry, error) {
	var m opsplan.Manifest
	byName := map[string]opsplan.FileEntry{}
	seen := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return opsplan.Manifest{}, nil, fmt.Errorf("%w: bad tar: %w", ErrBadArtifact, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Name == "MANIFEST" && m.Version() == "" {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return opsplan.Manifest{}, nil, fmt.Errorf("%w: bad manifest: %w", ErrBadArtifact, err)
			}
			m, err = opsplan.ParseManifest(b)
			if err != nil {
				return opsplan.Manifest{}, nil, fmt.Errorf("%w: %w", ErrBadArtifact, err)
			}
			for _, f := range m.Files {
				byName[f.Path] = f
			}
			continue
		}
		if e, ok := byName[h.Name]; ok {
			sum := sha256.New()
			if _, err := io.Copy(sum, io.LimitReader(tr, h.Size)); err != nil {
				return opsplan.Manifest{}, nil, fmt.Errorf("%w: %w", ErrBadArtifact, err)
			}
			if hex.EncodeToString(sum.Sum(nil)) != e.SHA256 {
				return opsplan.Manifest{}, nil, fmt.Errorf("%w: hash mismatch at %s", ErrBadArtifact, h.Name)
			}
			e.Size, e.Mode = h.Size, uint32(h.Mode&0o7777)
			byName[h.Name] = e
			seen[h.Name] = true // a listed file may be empty (the jail's dev/null): size alone cannot say it came
		}
	}
	if m.Version() == "" {
		return opsplan.Manifest{}, nil, fmt.Errorf("%w: no manifest", ErrBadArtifact)
	}
	out := make([]opsplan.FileEntry, 0, len(m.Files))
	for _, f := range m.Files {
		e, ok := byName[f.Path]
		if !ok || !seen[f.Path] {
			return opsplan.Manifest{}, nil, fmt.Errorf("%w: manifest names a file the tar lacks: %s", ErrBadArtifact, f.Path)
		}
		out = append(out, e)
	}
	return m, out, nil
}

// List answers a board's artifacts, newest first.
func (s *Service) List(ctx context.Context, board string) ([]ArtifactView, error) {
	rows, err := s.q.ListDeviceArtifacts(ctx, board)
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactView, 0, len(rows))
	for _, r := range rows {
		out = append(out, artifactView(r.ID, r.Board, r.Kind, r.Version, r.Label, r.Sha256, r.Size, r.Files, r.CreatedAt.Time))
	}
	return out, nil
}

func artifactView(id pgtype.UUID, board, kind, version, label, sha string, size int64, files []byte, at time.Time) ArtifactView {
	var fs []opsplan.FileEntry
	_ = json.Unmarshal(files, &fs)
	if fs == nil {
		fs = []opsplan.FileEntry{}
	}
	return ArtifactView{ID: id.String(), Board: board, Kind: kind, Version: version, Label: label,
		SHA256: sha, Size: size, Files: fs, CreatedAt: at}
}

// Delete drops an artifact.
func (s *Service) Delete(ctx context.Context, id string) error {
	uid, ok := uuid(id)
	if !ok {
		return ErrUnknownArtifact
	}
	n, err := s.q.DeleteDeviceArtifact(ctx, uid)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUnknownArtifact
	}
	return nil
}

// gc keeps a board's newest artifacts only.
func (s *Service) gc(ctx context.Context, board string) {
	rows, err := s.q.ListDeviceArtifacts(ctx, board)
	if err != nil {
		s.log.Error("ops_gc_failed", "board", board)
		return
	}
	for i, r := range rows {
		if i < keepPerBoard {
			continue
		}
		if _, err := s.q.DeleteDeviceArtifact(ctx, r.ID); err != nil {
			s.log.Error("ops_gc_failed", "board", board)
			return
		}
		s.log.Info("ops_artifact_dropped", "board", board, "version", r.Version)
	}
}

// artifact is a run's artifact: its whole blob and, for a tar, the members a yandex plan fetches,
// pulled out once.
type artifact struct {
	board     string
	kind      string
	version   string
	sha       string
	downgrade bool // an owner's rollback installs an older build
	data      []byte
	files     []opsplan.FileEntry
	members   map[string][]byte
}

func (s *Service) loadArtifact(ctx context.Context, id string) (*artifact, error) {
	uid, ok := uuid(id)
	if !ok {
		return nil, ErrUnknownArtifact
	}
	r, err := s.q.GetDeviceArtifact(ctx, uid)
	if err != nil {
		return nil, ErrUnknownArtifact
	}
	a := &artifact{board: r.Board, kind: r.Kind, version: r.Version, sha: r.Sha256, data: r.Data}
	_ = json.Unmarshal(r.Files, &a.files)
	return a, nil
}

// open answers the bytes a fetch step's src names: the whole blob ("artifact:") or a tar member
// ("artifact:bin/cadenced").
func (a *artifact) open(src string) ([]byte, error) {
	if src == "artifact:" {
		return a.data, nil
	}
	name, ok := strings.CutPrefix(src, "artifact:")
	if !ok {
		return nil, fmt.Errorf("ops artifact: bad src %q", src)
	}
	if a.members == nil {
		a.members = map[string][]byte{}
		if err := a.extract(name); err != nil {
			return nil, err
		}
	}
	b, ok := a.members[name]
	if !ok {
		if err := a.extract(name); err != nil {
			return nil, err
		}
		b = a.members[name]
	}
	if b == nil {
		return nil, fmt.Errorf("ops artifact: no member %q", name)
	}
	return b, nil
}

// extract pulls name out of the tar once; a miss stores nil so it is not looked for again.
func (a *artifact) extract(name string) error {
	tr := tar.NewReader(bytes.NewReader(a.data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			a.members[name] = nil
			return nil
		}
		if err != nil {
			return fmt.Errorf("ops artifact: bad tar: %w", err)
		}
		if h.Typeflag == tar.TypeReg && h.Name == name {
			b, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("ops artifact: %w", err)
			}
			a.members[name] = b
			return nil
		}
	}
}
