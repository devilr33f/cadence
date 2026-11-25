package yandexmusic

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// accountUID reads the account id once for library likes and dislikes.
func (c *Client) accountUID(ctx context.Context) (int64, error) {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	if c.accountID > 0 {
		return c.accountID, nil
	}
	var r struct {
		Account struct {
			UID int64 `json:"uid"`
		} `json:"account"`
	}
	if err := c.get(ctx, "/account/status", nil, true, &r); err != nil {
		return 0, err
	}
	if r.Account.UID <= 0 {
		return 0, errBadAnswer
	}
	c.accountID = r.Account.UID
	return c.accountID, nil
}

// RateTrack updates the account library. Adding either mark removes the other
// on Yandex's side; clearing removes both so a local toggle can start either way.
func (c *Client) RateTrack(ctx context.Context, track providers.Track, rating providers.TrackRating) error {
	if track.Source != providers.SourceYandex || !idRe.MatchString(track.ID) || !providers.ValidTrackRating(rating) {
		return errFailed
	}
	var cause error
	if c.guard(func() error {
		uid, err := c.accountUID(ctx)
		if err != nil {
			cause = err
			return err
		}
		base := "/users/" + strconv.FormatInt(uid, 10)
		var paths []string
		switch rating {
		case providers.RatingLike:
			paths = append(paths, base+"/likes/tracks/add-multiple")
		case providers.RatingDislike:
			paths = append(paths, base+"/dislikes/tracks/add-multiple")
		case providers.RatingNone:
			paths = append(paths, base+"/likes/tracks/remove", base+"/dislikes/tracks/remove")
		}
		for _, path := range paths {
			var answer struct {
				Revision *int64 `json:"revision"`
			}
			cause = c.call(ctx, http.MethodPost, path, nil, url.Values{"track-ids": {track.ID}}, true, &answer)
			if cause == nil && answer.Revision == nil {
				cause = errBadAnswer
			}
			if cause != nil {
				return cause
			}
		}
		return nil
	}) != nil {
		return feedbackError(cause)
	}
	return nil
}

// ReportListening sends one station event as Yandex's radio clients do. The
// initial track of a track radio has no batch yet and derives its station here.
func (c *Client) ReportListening(ctx context.Context, event providers.ListeningEvent) error {
	track := event.Track
	station := track.Station
	if station == "" && track.Source == providers.SourceYandex && idRe.MatchString(track.ID) {
		station = "track:" + track.ID
	}
	if track.Source != providers.SourceYandex || !idRe.MatchString(track.ID) || !validStation(station) ||
		len(track.StationBatch) > 200 || strings.ContainsAny(track.StationBatch, "\x00\r\n") ||
		math.IsNaN(event.PlayedS) || math.IsInf(event.PlayedS, 0) || event.PlayedS < 0 {
		return errFailed
	}
	switch event.Kind {
	case providers.ListeningRadioStarted, providers.ListeningTrackStarted, providers.ListeningTrackFinished, providers.ListeningTrackSkipped:
	default:
		return errFailed
	}
	at := event.At
	if at.IsZero() {
		at = c.cfg.Now()
	}
	body := map[string]any{"type": string(event.Kind), "timestamp": at.UTC().Format(time.RFC3339)}
	if event.Kind != providers.ListeningRadioStarted {
		body["trackId"] = track.ID
	} else {
		body["from"] = "cadence-server"
	}
	if (event.Kind == providers.ListeningTrackFinished || event.Kind == providers.ListeningTrackSkipped) && event.PlayedS > 0 {
		body["totalPlayedSeconds"] = event.PlayedS
	}
	q := url.Values{}
	if track.StationBatch != "" {
		q.Set("batch-id", track.StationBatch)
	}
	var cause error
	if c.guard(func() error {
		var answer string
		cause = c.postJSON(ctx, "/rotor/station/"+station+"/feedback", q, body, &answer)
		if cause == nil && answer != "ok" {
			cause = errBadAnswer
		}
		return cause
	}) != nil {
		return feedbackError(cause)
	}
	return nil
}

func feedbackError(err error) error {
	if authFailure(err) {
		return providers.ErrSignInRequired
	}
	return errors.New(providers.CodeMusicFailed)
}
