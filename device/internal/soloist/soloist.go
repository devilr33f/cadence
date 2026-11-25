// Package soloist is cadenced's side of Spotify's Soloist under audiod
// (M4c-D2): the media.soloist capability and its api key, the sender-volume
// policy (rule 7), and the updater that keeps the build fresh.
package soloist

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/receivername"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// Options are the board's: where soloist lives and how it is reached.
type Options struct {
	// Dir holds soloist's builds and runtime: the board's <data dir>/soloist.
	Dir string
	// ScratchDir is audiod's scratch for soloist: the board's runtime dir + /soloist.
	ScratchDir string
	// CAFile is the bundle bound into soloist's jail.
	CAFile string
	// Updates is where builds come from; nil never downloads one.
	Updates *Updates
	// Ringing tells whether a ring sounds, when no sender moves the level; nil
	// is never.
	Ringing func() bool
}

// Updates are the board's CDN.
type Updates struct {
	// URL serves the tarball over https: updater.SpotifyARM64 on an arm64 board.
	URL string
	// CAFile is the bundle the CDN is verified against, alone.
	CAFile string
	// Machine is the ELF e_machine of the board's builds.
	Machine uint16
}

// Soloist keeps what audiod forgets with its client (the config and the key),
// moves the level for soloist's slider and keeps the build fresh.
type Soloist struct {
	vol    *volume.Setter
	opts   Options
	log    *slog.Logger
	cap    *provider
	policy *policy
	upd    *updater.Updater // nil without a CDN
	// kicks the updates loop: the config or audiod's status changed
	updKick chan struct{}

	// held while a config or a level goes out, so audiod gets them in order
	sendMu sync.Mutex

	mu      sync.Mutex
	cfg     config
	device  string                         // the device's name, "" while it has none
	key     string                         // a valid key, else ""
	problem string                         // why key is "": missing or invalid
	send    func(*audiodv1.Envelope) error // the live connection's, nil while audiod is down
	status  *audiodv1.SoloistStatus        // the latest on it
	shown   state                          // the state published last
	// the installed build as the updater last read it, when it has
	installed      *updater.Build
	installedKnown bool
	// for the updates loop, since it last looked: an edge into no_build or
	// expired, and the lowest silent-start count seen
	noBuild    bool
	expired    bool
	silentSeen bool
	silentMin  uint32
	probes     map[string]chan *audiodv1.SoloistProbeResult
	probeSeq   uint64
}

// New starts with soloist off. A nil log discards.
func New(vol *volume.Setter, opts Options, log *slog.Logger) *Soloist {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Soloist{
		vol: vol, opts: opts, log: log, problem: keyMissing,
		updKick: make(chan struct{}, 1), probes: map[string]chan *audiodv1.SoloistProbeResult{},
	}
	s.cap = &provider{s: s, state: make(chan json.RawMessage, 1)}
	s.policy = newPolicy(vol, opts.Ringing, s.sendLevel, log)
	if u := opts.Updates; u != nil && u.URL != "" {
		s.upd = &updater.Updater{
			Dir: opts.Dir, Fetch: &updater.CDN{URL: u.URL, CAFile: u.CAFile}, Probe: prober{s}, Machine: u.Machine, Now: time.Now,
		}
	}
	s.mu.Lock()
	s.publishLocked()
	s.mu.Unlock()
	return s
}

// Capability is media.soloist.
func (s *Soloist) Capability() capability.Provider { return s.cap }

// Run moves the level for soloist's slider and keeps the build fresh until
// ctx ends and an update running returned.
func (s *Soloist) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { s.policy.run(ctx) })
	if s.upd != nil {
		wg.Go(func() { s.runUpdates(ctx) })
	}
	wg.Wait()
}

// configure takes a config that applied: its key goes to audiod only with
// soloist enabled
func (s *Soloist) configure(cfg config, key, problem string) {
	if cfg.Enabled && problem != "" {
		s.log.Warn("soloist_key_missing", "reason", problem)
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.cfg, s.key, s.problem = cfg, key, problem
	send := s.send
	s.publishLocked()
	s.mu.Unlock()
	s.kickUpdates()
	if send != nil {
		_ = send(s.configEnvelope())
	}
}

// SetDeviceName is the device's name, as the server last named it ("" for none yet): soloist's name while its config
// sets none. A change of the name it advertises goes to audiod at once (audiod restarts a running soloist for it);
// the same name sends nothing.
func (s *Soloist) SetDeviceName(device string) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	was := s.nameLocked()
	s.device = device
	send := s.send
	changed := s.nameLocked() != was
	s.mu.Unlock()
	if changed && send != nil {
		_ = send(s.configEnvelope())
	}
}

// the name phones see; callers hold mu
func (s *Soloist) nameLocked() string { return receivername.Pick(s.cfg.Name, s.device, maxName) }

func (s *Soloist) configEnvelope() *audiodv1.Envelope {
	level, known := s.vol.Current()
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &audiodv1.SoloistConfig{
		DeviceName: s.nameLocked(), Dir: s.opts.Dir, ScratchDir: s.opts.ScratchDir, CaFile: s.opts.CAFile, InitialVolume: unknownLevel,
	}
	if s.cfg.Enabled && s.key != "" {
		c.Enabled, c.ApiKey = true, s.key
	}
	if known {
		c.InitialVolume = uint32(level) //nolint:gosec // the setter keeps 0-100
	}
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistConfig{SoloistConfig: c}}
}

// SenderVolume takes the slider move of a sender cadenced hears itself (bluetooth's avrcp volume, as 0-100), with
// echoes and first values already dropped; it never blocks.
func (s *Soloist) SenderVolume(sender audiodv1.Sender, level uint32) { s.policy.request(sender, level) }

// FollowLevel has f run after every change of the level and every refused slider move, on the policy's goroutine
// once audiod's senders were told: it reads the level in force itself, and may block.
func (s *Soloist) FollowLevel(f func()) { s.policy.follow(f) }
