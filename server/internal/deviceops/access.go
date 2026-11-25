package deviceops

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// access is one out-of-band way the server reaches a device: adb over the lan, ssh to the board's
// host. The ssh private key is sealed with the master key (rule 6): never logged, never answered.
type access struct {
	deviceID, addr, sshKey, sshHostKey string
}

// AccessView is an access row as the dashboard sees it (never the key).
type AccessView struct {
	Kind      string     `json:"kind"`
	Addr      string     `json:"addr"`
	HostKey   string     `json:"hostKey,omitempty"`
	LastProbe *time.Time `json:"lastProbe,omitempty"`
	LastOK    *bool      `json:"lastOk,omitempty"`
}

func (s *Service) access(ctx context.Context, deviceID string) (map[string]access, error) {
	uid, ok := uuid(deviceID)
	if !ok {
		return nil, ErrUnknownDevice
	}
	rows, err := s.q.ListDeviceAccess(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := map[string]access{}
	for _, r := range rows {
		a := access{deviceID: deviceID, addr: r.Addr, sshHostKey: r.SshHostKey}
		if r.Kind == TransportSSH {
			key, err := s.q.GetDeviceAccessKey(ctx, uid)
			if err == nil && key != "" {
				if plain, err := s.box.Open([]byte(key), accessAAD(deviceID)); err == nil {
					a.sshKey = string(plain)
				}
			}
		}
		out[r.Kind] = a
	}
	return out, nil
}

// Access lists a device's access rows.
func (s *Service) Access(ctx context.Context, deviceID string) ([]AccessView, error) {
	uid, ok := uuid(deviceID)
	if !ok {
		return nil, ErrUnknownDevice
	}
	rows, err := s.q.ListDeviceAccess(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := make([]AccessView, 0, len(rows))
	for _, r := range rows {
		v := AccessView{Kind: r.Kind, Addr: r.Addr, HostKey: r.SshHostKey}
		if r.LastProbe.Valid {
			t := r.LastProbe.Time
			v.LastProbe = &t
		}
		if r.LastOk.Valid {
			b := r.LastOk.Bool
			v.LastOK = &b
		}
		out = append(out, v)
	}
	return out, nil
}

// SetAccess stores one access row; the ssh key, when given, is sealed before it is stored. An
// empty addr removes the row.
func (s *Service) SetAccess(ctx context.Context, deviceID, kind, addr, sshKey, hostKey string) error {
	if kind != TransportAdb && kind != TransportSSH {
		return fmt.Errorf("%w: kind %q", ErrBadAccess, kind)
	}
	uid, ok := uuid(deviceID)
	if !ok {
		return ErrUnknownDevice
	}
	if addr == "" {
		_, err := s.q.DeleteDeviceAccess(ctx, db.DeleteDeviceAccessParams{DeviceID: uid, Kind: kind})
		return err
	}
	if err := validateAddr(kind, addr); err != nil {
		return err
	}
	if kind == TransportAdb {
		sshKey, hostKey = "", ""
	}
	if sshKey != "" {
		if s.box == nil {
			return fmt.Errorf("%w: no master key", ErrBadAccess)
		}
		sealed, err := s.box.Seal([]byte(sshKey), accessAAD(deviceID))
		if err != nil {
			return fmt.Errorf("%w: key", ErrBadAccess)
		}
		sshKey = string(sealed)
	} else {
		// keeping the stored key when the request carries none: a re-save of the address alone
		if old, err := s.q.GetDeviceAccessKey(ctx, uid); err == nil && old != "" && kind == TransportSSH {
			sshKey = old
		}
	}
	return s.q.PutDeviceAccess(ctx, db.PutDeviceAccessParams{
		DeviceID: uid, Kind: kind, Addr: addr, SshKey: sshKey, SshHostKey: hostKey,
	})
}

// DeleteAccess drops one access row.
func (s *Service) DeleteAccess(ctx context.Context, deviceID, kind string) error {
	uid, ok := uuid(deviceID)
	if !ok {
		return ErrUnknownDevice
	}
	_, err := s.q.DeleteDeviceAccess(ctx, db.DeleteDeviceAccessParams{DeviceID: uid, Kind: kind})
	return err
}

// ProbeAccess tries one access row and records how it went, so the dashboard can say a device's
// adb address is stale (the Lite moved twice already).
func (s *Service) ProbeAccess(ctx context.Context, deviceID, kind string) error {
	accs, err := s.access(ctx, deviceID)
	if err != nil {
		return err
	}
	acc, ok := accs[kind]
	if !ok || acc.addr == "" {
		return ErrNoTransport
	}
	var tr transport
	switch kind {
	case TransportAdb:
		tr = &adbTransport{server: s.adb}
	case TransportSSH:
		tr = &sshTransport{box: s.box, q: s.q}
	default:
		return ErrNoTransport
	}
	err = tr.probe(ctx, acc)
	uid, _ := uuid(deviceID)
	okay := err == nil
	_ = s.q.TouchDeviceAccess(ctx, db.TouchDeviceAccessParams{DeviceID: uid, Kind: kind, Now: ts(s.now()), Ok: pgtype.Bool{Bool: okay, Valid: true}})
	if err != nil {
		s.log.Info("ops_access_probe_failed", "device_id", deviceID, "kind", kind)
	}
	return err
}

func accessAAD(deviceID string) []byte { return []byte("device-access:" + deviceID) }

// validateAddr keeps the shapes the transports dial: adb "host:port", ssh "[user@]host[:port]".
func validateAddr(kind, addr string) error {
	if kind == TransportAdb {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host == "" || port == "" || strings.ContainsAny(host, " /'`\"") {
			return fmt.Errorf("%w: adb wants host:port", ErrBadAccess)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("%w: adb wants host:port", ErrBadAccess)
		}
		return nil
	}
	rest := strings.TrimPrefix(addr, "ssh://")
	host := rest
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		host = rest[at+1:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || strings.ContainsAny(host, " /'\"") {
		return fmt.Errorf("%w: ssh wants [user@]host[:port]", ErrBadAccess)
	}
	return nil
}
