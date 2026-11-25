package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// MediaPlayArgs is rpc media.play's arguments: a play a screen chose from the catalog it was handed.
type MediaPlayArgs struct {
	Ref     catalog.Ref         `json:"ref"`
	Episode string              `json:"episode,omitempty"`
	Choices catalog.Selection   `json:"choices,omitempty"`
	From    catalog.ResolveFrom `json:"from,omitempty"`
	// StartMS > 0 starts there instead of the resume point (a voiceover switch keeps the position)
	StartMS int64 `json:"startMs,omitempty"`
	// Replaces is the session this play follows (a switch, the next episode); the new session displaces it
	Replaces string `json:"replaces,omitempty"`
	// Reason is user, switch or continue
	Reason string `json:"reason,omitempty"`
}

// MediaPlayAnswer is media.play's result.
type MediaPlayAnswer struct {
	SessionID string `json:"sessionId"`
	Episode   string `json:"episode,omitempty"`
}

var playReasons = map[string]bool{"": true, "user": true, "switch": true, "continue": true}

// PlayMethod serves rpc media.play on the screen that calls it: the same resolve and media.video/play as catalog_play,
// so the session is the server's and cadenced's like any other. A deliberate press on the remote, it plays only refs
// this server issued and never touches the volume. Arguments are never logged.
func (d ScreenDeps) PlayMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		var a MediaPlayArgs
		if !strictJSON(c.Args, &a) || a.Ref == "" || len(a.Ref) > 256 || len(a.Episode) > 32 || len(a.Choices) > 8 ||
			(a.From != "" && a.From != catalog.FromStart && a.From != catalog.FromResume) || a.StartMS < 0 ||
			len(a.Replaces) > 64 || !playReasons[a.Reason] {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		if d.Catalog == nil {
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		from, start := a.From, int64(-1)
		if from == "" {
			from = catalog.FromResume
		}
		if a.StartMS > 0 {
			start = a.StartMS
		}
		if a.Replaces != "" && d.Current != nil {
			cur, pos, ok := d.Current(c.DeviceID)
			switch {
			case ok && cur != a.Replaces:
				// something else plays now (the owner chose it): a switch or an auto-continue of the old one must
				// not displace it
				return calls.Fail(rpc.StatusExpired, "superseded")
			case ok && a.Reason == "switch" && start < 0 && pos > 0:
				// a switch keeps the position the replaced session is at
				start = pos
			}
		}
		if a.Reason == "continue" && a.From == "" && a.StartMS == 0 {
			// the next episode starts at its start, not where an earlier watch left it
			from = catalog.FromStart
		}
		if c.Event != nil {
			ctx = ytdlp.WithProgress(ctx, func(ev ytdlp.ResolveEvent) { _ = c.Event(ev) })
		}
		ep, session, e := d.Play(ctx, c.DeviceID, a.Ref, a.Episode, a.Choices, from, start)
		if e != nil {
			return calls.Fail(playStatus(e.Code), e.Code)
		}
		if a.Replaces != "" && a.Replaces != session && d.Replaced != nil {
			// the device's new session displaced it; the server forgets what it kept of it
			d.Replaced(c.DeviceID, a.Replaces)
		}
		return calls.OK(MediaPlayAnswer{SessionID: session, Episode: ep})
	}
}

func playStatus(code string) string {
	switch code {
	case "unknown_ref":
		return rpc.StatusExpired
	case "not_found":
		return rpc.StatusNotFound
	case "unplayable", "refused", "sign_in_required", "unsupported":
		return rpc.StatusRefused
	case "no_screen":
		return rpc.StatusInvalid
	}
	return rpc.StatusUnavailable
}

// strictJSON reads one JSON object with no unknown field and nothing after it
func strictJSON(raw []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	var rest json.RawMessage
	return dec.Decode(&rest) == io.EOF
}
