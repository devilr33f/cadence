package cards

import (
	"context"
	"strconv"
	"sync"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

// MediaPlay is a music_play or video_play call's live card. Its id stays with
// the call while resolve stages, buffering and the terminal result replace its
// contents. Item text is shown only, never logged.
type MediaPlay struct {
	info    playprogress.CardInfo
	artwork string

	mu                sync.Mutex
	update            playprogress.Update
	changed           chan struct{}
	version, rendered uint64
}

func NewMediaPlay(info playprogress.CardInfo, artwork string) *MediaPlay {
	return &MediaPlay{info: info, artwork: artwork, changed: make(chan struct{}),
		update: playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageCookies, RemainingMS: -1},
			State: playprogress.Resolving, BufferingPercent: -1}}
}

func (c *MediaPlay) ID() string { return OpaqueID("media-play", c.info.ID) }

func (c *MediaPlay) ForTool() string { return c.info.Tool }

// Set replaces the card's progress. A terminal first frame or failure cannot
// be moved backwards by a late stderr or relay notification.
func (c *MediaPlay) Set(u playprogress.Update) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.update.State == playprogress.Playing || c.update.State == playprogress.Failed {
		return
	}
	if u.State == "" {
		u.State = playprogress.Resolving
	}
	c.update = u
	c.version++
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *MediaPlay) Changed(ctx context.Context) bool {
	c.mu.Lock()
	ch, news := c.changed, c.version != c.rendered
	c.mu.Unlock()
	if news {
		return true
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *MediaPlay) Follow() time.Duration { return 2 * time.Minute }

func (c *MediaPlay) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	c.mu.Lock()
	info, art, u := c.info, c.artwork, c.update
	c.rendered = c.version
	c.mu.Unlock()

	// a list's play names the song it fetches once its songs are read
	if it := u.Item; it != nil && it.Title != "" {
		info.Title, info.Artist, art = it.Title, it.Artist, it.ArtworkRef
	}
	title := clip(info.Title, 80)
	if title == "" {
		title = stepLabel(r, info.Tool)
	}
	label := mediaStageLabel(r, info.Kind, u)
	kind, tone := cardsv1.StateKind_STATE_KIND_UNKNOWN, cardsv1.Tone_TONE_NEUTRAL
	switch u.State {
	case playprogress.Playing:
		kind = cardsv1.StateKind_STATE_KIND_ON
	case playprogress.Failed:
		kind, tone = cardsv1.StateKind_STATE_KIND_UNAVAILABLE, cardsv1.Tone_TONE_WARNING
	}
	icon := "film"
	if info.Tool == "music_play" {
		icon = "music"
	}
	b := &cardsv1.CardBlock{Title: title, Subtitle: clip(joined(" · ", info.Artist, info.DeviceName), 100), Icon: icon, Tone: tone}
	infoNodes := pcards.Nodes(&cardsv1.State{Label: clip(label, 100), Kind: kind})
	if imageIDLike(art) {
		b.Children = pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_M, Children: pcards.Nodes(
			&cardsv1.Image{Ref: "img:" + art, Alt: title, Aspect: cardsv1.Aspect_ASPECT_SQUARE},
			&cardsv1.Column{Gap: cardsv1.Gap_GAP_S, Children: infoNodes})})
	} else {
		b.Children = infoNodes
	}
	if u.State == playprogress.Resolving || u.State == playprogress.Buffering {
		progress := &cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}
		if u.State == playprogress.Resolving {
			progress.Amount = &cardsv1.Progress_Fraction{Fraction: mediaStageFraction(u.Stage)}
		} else if u.BufferingPercent >= 0 && u.BufferingPercent <= 100 {
			progress.Amount = &cardsv1.Progress_Fraction{Fraction: float64(u.BufferingPercent) / 100}
		}
		b.Children = append(b.Children, pcards.N(progress))
	}
	fallback := joined(" · ", title, info.Artist, label)
	return pcards.NewCard("", 0, clip(fallback, 280), b), nil
}

func mediaStageLabel(r *Render, kind string, u playprogress.Update) string {
	if u.State == playprogress.Playing {
		return r.T("catalog.playing")
	}
	if u.State == playprogress.Buffering {
		label := r.T("catalog.buffering")
		if u.BufferingPercent >= 0 && u.BufferingPercent <= 100 {
			label += " " + strconv.Itoa(u.BufferingPercent) + " %"
		}
		return label
	}
	stage := mediaResolveStage(r, kind, u)
	if u.State == playprogress.Failed {
		return r.T("resolving.failed_stage", "stage", stage) + " · " + reason(r, u.Code)
	}
	return stage
}

func mediaResolveStage(r *Render, kind string, u playprogress.Update) string {
	switch u.Stage {
	case playprogress.StageCollection:
		switch kind {
		case playprogress.KindAlbum:
			return r.T("resolving.collection_album")
		case playprogress.KindArtist:
			return r.T("resolving.collection_artist")
		}
		return r.T("resolving.collection_playlist")
	case playprogress.StageCookies:
		return r.T("resolving.cookies")
	case playprogress.StageExtract:
		return r.T("resolving.extract")
	case playprogress.StageChallenge:
		return r.T("resolving.challenge")
	case playprogress.StageFormats:
		return r.T("resolving.formats")
	case playprogress.StagePick:
		label := r.T("resolving.pick")
		if u.Height > 0 {
			label = strconv.Itoa(u.Height) + "p"
			if u.VCodec != "" {
				label += " · " + u.VCodec
			}
		}
		if u.AudioTracks > 0 {
			label += " · " + r.T("resolving.with_audio")
		}
		return label
	case playprogress.StageWaitAvailable:
		seconds := max(int64(0), (u.RemainingMS+999)/1000)
		return r.T("resolving.wait_available", "seconds", strconv.FormatInt(seconds, 10))
	case playprogress.StageRelay:
		return r.T("resolving.relay")
	}
	return r.T("catalog.starting")
}

func mediaStageFraction(stage playprogress.Stage) float64 {
	switch stage {
	case playprogress.StageCollection:
		return 0.04
	case playprogress.StageCookies:
		return 0.08
	case playprogress.StageExtract:
		return 0.22
	case playprogress.StageChallenge:
		return 0.38
	case playprogress.StageFormats:
		return 0.55
	case playprogress.StagePick:
		return 0.7
	case playprogress.StageWaitAvailable:
		return 0.82
	case playprogress.StageRelay:
		return 0.94
	}
	return 0.02
}
