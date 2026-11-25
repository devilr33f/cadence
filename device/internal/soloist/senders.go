package soloist

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// Senders is rule 7's media sender policy without soloist itself: the screens run libairplay and no soloist, and its
// slider moves the level as v1's senders did. It is audiod's soloist handler there (audiod.WithSoloist), so it sends
// no soloist config: only the speaker's level after each Ready and on change, and it takes SenderVolume alone.
type Senders struct {
	vol    *volume.Setter
	policy *policy

	sendMu sync.Mutex
	mu     sync.Mutex
	send   func(*audiodv1.Envelope) error
}

// NewSenders moves vol for a sender's slider; ringing, when not nil, refuses it while a ring sounds. A nil log
// discards.
func NewSenders(vol *volume.Setter, ringing func() bool, log *slog.Logger) *Senders {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Senders{vol: vol}
	s.policy = newPolicy(vol, ringing, s.sendLevel, log)
	return s
}

// Run handles the sliders until ctx ends.
func (s *Senders) Run(ctx context.Context) { s.policy.run(ctx) }

// SoloistReady tells a new audiod connection the level, when the device has one, then uses it.
func (s *Senders) SoloistReady(send func(*audiodv1.Envelope) error) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if level, known := s.vol.Current(); known {
		for _, e := range levelEnvelopes(level) {
			_ = send(e)
		}
	}
	s.mu.Lock()
	s.send = send
	s.mu.Unlock()
}

// SoloistEnvelope takes a SenderVolume; it never blocks.
func (s *Senders) SoloistEnvelope(env *audiodv1.Envelope) {
	if v := env.GetSenderVolume(); v != nil {
		s.policy.request(v.GetSender(), v.GetLevel())
	}
}

// SoloistGone follows the end of the connection SoloistReady took.
func (s *Senders) SoloistGone() {
	s.mu.Lock()
	s.send = nil
	s.mu.Unlock()
}

func (s *Senders) sendLevel() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	send := s.send
	s.mu.Unlock()
	if level, known := s.vol.Current(); send != nil && known {
		for _, e := range levelEnvelopes(level) {
			_ = send(e)
		}
	}
}

// SenderVolume takes the slider move of a sender cadenced hears itself (bluetooth's avrcp volume, as 0-100), with
// echoes and first values already dropped; it never blocks.
func (s *Senders) SenderVolume(sender audiodv1.Sender, level uint32) { s.policy.request(sender, level) }

// FollowLevel has f run after every change of the level and every refused slider move, on the policy's goroutine
// once audiod's senders were told: it reads the level in force itself, and may block.
func (s *Senders) FollowLevel(f func()) { s.policy.follow(f) }
