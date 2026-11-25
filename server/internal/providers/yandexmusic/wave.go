package yandexmusic

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// My Wave is the account's personal station, called Моя волна, My Wave or My Vibe.
const myWaveStation = "user:onyourwave"

var trackStationRe = regexp.MustCompile(`^track:[0-9]{1,20}$`)

func validStation(station string) bool {
	return station == myWaveStation || trackStationRe.MatchString(station)
}

func myWave() providers.Collection {
	return providers.Collection{
		Source: providers.SourceYandex, Kind: providers.KindStation, ID: myWaveStation,
		Title: "Моя волна", Owner: "Yandex Music",
	}
}

// stationSongs reads a personal station's next batch with the account's token.
// previous is the song the last batch ended on, which keeps the next one moving.
func (c *Client) stationSongs(ctx context.Context, station, previous string, limit int) ([]providers.Track, error) {
	if !validStation(station) || limit < 1 {
		return nil, errFailed
	}
	limit = min(limit, maxListSongs)
	var out []providers.Track
	var cause error
	if c.guard(func() error {
		var r struct {
			Sequence []struct {
				Track trackItem `json:"track"`
			} `json:"sequence"`
			BatchID string `json:"batchId"`
		}
		q := url.Values{}
		if previous != "" {
			q.Set("queue", previous)
		} else {
			q.Set("settings2", "true")
		}
		cause = c.get(ctx, "/rotor/station/"+station+"/tracks", q, station == myWaveStation, &r)
		if cause != nil {
			return cause
		}
		if len(r.BatchID) > 200 || strings.ContainsAny(r.BatchID, "\x00\r\n") {
			return errBadAnswer
		}
		items := make([]trackItem, 0, len(r.Sequence))
		for _, s := range r.Sequence {
			if string(s.Track.ID) != previous {
				items = append(items, s.Track)
			}
		}
		out = tracks(items, limit)
		for i := range out {
			out[i].Station = station
			out[i].StationBatch = r.BatchID
		}
		return nil
	}) != nil {
		if authFailure(cause) {
			return nil, providers.ErrSignInRequired
		}
		return nil, errFailed
	}
	return out, nil
}
