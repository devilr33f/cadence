package app

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/remote"
)

type remoteDeps struct {
	enrolled *enroll.Service
	groups   *groups.Service
	sessions *media.Store
	caps     *devices.Service
	cmds     *commands.Service
	images   *images.Proxy
	artwork  *media.Artwork
	origin   string
	parts    screenParts
	log      *slog.Logger
}

// buildRemote registers devices.media and devices.panel: what the household's other devices play, on a screen
func buildRemote(d remoteDeps) *remote.Service {
	s := remote.New(remote.Deps{
		Devices: func(ctx context.Context) ([]remote.Device, error) {
			ds, err := d.enrolled.List(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]remote.Device, len(ds))
			for i, x := range ds {
				out[i] = remote.Device{ID: x.ID, Name: x.Name, Room: x.Room}
			}
			return out, nil
		},
		Groups: func(ctx context.Context) ([]remote.Group, error) {
			gs, err := d.groups.List(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]remote.Group, len(gs))
			for i, g := range gs {
				out[i] = remote.Group{ID: g.ID, Name: g.Name, Kind: g.Kind, Leader: d.groups.Leader(g.ID)}
				for _, m := range g.Members {
					out[i].Members = append(out[i].Members, m.DeviceID)
				}
			}
			return out, nil
		},
		Session: d.sessions.Get,
		Level: func(ctx context.Context, dev string) (int, bool) {
			raw, ok := d.caps.State(ctx, dev, capschema.AudioOutputID)
			if !ok {
				return 0, false
			}
			var st struct {
				Level *int `json:"level"`
			}
			if json.Unmarshal(raw, &st) != nil || st.Level == nil {
				return 0, false
			}
			return *st.Level, true
		},
		Groupable: func(ctx context.Context, dev string) bool {
			_, err := d.caps.Desired(ctx, dev, capschema.MediaGroupID)
			return err == nil
		},
		CardFeatures: func(ctx context.Context, dev string) []string {
			raw, ok := d.caps.State(ctx, dev, capschema.DisplayScreenID)
			if !ok {
				return nil
			}
			var st struct {
				CardFeatures []string `json:"cardFeatures"`
			}
			if json.Unmarshal(raw, &st) != nil {
				return nil
			}
			return st.CardFeatures
		},
		Art:     remoteArt(d.origin, d.images, d.artwork),
		Command: d.cmds.Send,
		Join: func(ctx context.Context, lead string, others []string) error {
			_, err := d.groups.Join(ctx, lead, others)
			return err
		},
		Attach: func(ctx context.Context, member, dev string) error {
			_, err := d.groups.Attach(ctx, member, dev)
			return err
		},
		Leave: d.groups.Leave,
		Cards: d.parts.cards, Log: d.log,
	})
	d.parts.router.Register(rpc.DevicesMedia, s.MediaMethod())
	d.parts.router.Register(rpc.DevicesPanel, s.PanelMethod())
	return s
}

// remoteArt is a session's cover as the image proxy's img: ref: the server's own proxied image by its id, any other
// url through the proxy, and a cover the device uploaded (airplay) through the proxy's loader while it is held
func remoteArt(origin string, p *images.Proxy, art *media.Artwork) func(dev string, s media.MediaSession) string {
	own := origin + "/v2/images/"
	return func(dev string, s media.MediaSession) string {
		md := s.Metadata
		if u := md.ArtworkURL; u != "" {
			if id, ok := strings.CutPrefix(u, own); origin != "" && ok && images.IDPattern.MatchString(id) {
				return "img:" + id
			}
			if id := p.Ref(u, false); id != "" {
				return "img:" + id
			}
			return ""
		}
		if md.ArtworkHash == "" || art == nil {
			return ""
		}
		// a group member's cover is its stream source's upload
		dev = cmp.Or(s.ArtworkDevice, dev)
		if _, _, ok := art.Uploaded(dev, md.ArtworkHash); !ok {
			return ""
		}
		hash := md.ArtworkHash
		return "img:" + p.RefLoaded(dev+"\x00"+hash, func() (string, []byte, error) {
			ct, data, ok := art.Uploaded(dev, hash)
			if !ok {
				return "", nil, images.ErrUnavailable
			}
			return ct, data, nil
		})
	}
}
