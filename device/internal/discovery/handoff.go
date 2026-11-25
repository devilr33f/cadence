package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

// Handoff is what a board whose system answers mDNS itself (android: NsdManager in the app) is given to advertise:
// the instance name and the TXT keys, as Advertise would put them on the wire. The app registers it, cadenced only
// says what and while when.
type Handoff struct {
	// the DNS-SD instance name, without the service type
	Name string `json:"name"`
	// the TXT record: proto, id, board and ver
	TXT map[string]string `json:"txt"`
}

// HandoffOf is the advertisement Advertise would answer with for info.
func HandoffOf(info Info) Handoff {
	return Handoff{
		Name: "Cadence " + labelSafe(info.Board) + " " + shortID(info.DeviceID),
		TXT:  map[string]string{keyProto: txtProto, keyID: info.DeviceID, keyBoard: info.Board, keyVersion: info.Version},
	}
}

// AdvertiseHandoff advertises by writing the Handoff to path (mode 0600, replaced whole) while ctx lasts and removing
// it at the end: a responder outside cadenced registers the service while the file is there. Its directory is made
// 0700.
func AdvertiseHandoff(path string) AdvertiseFunc {
	return func(ctx context.Context, info Info, log *slog.Logger, ready func()) error {
		raw, err := json.Marshal(HandoffOf(info))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := atomicfile.Write(path, raw); err != nil {
			return err
		}
		log.Info("discovery_handed_off")
		if ready != nil {
			ready()
		}
		<-ctx.Done()
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("discovery_handoff_left", "reason", "remove")
		}
		return nil
	}
}
