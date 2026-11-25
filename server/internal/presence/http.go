package presence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const tag = "presence"

// maxTTLSeconds is MaxTTL in the API's unit.
const maxTTLSeconds = int(MaxTTL / time.Second)

// PutBusyRequest is a source's busy signal: why, which devices (none: every
// device) and for how long; a source that keeps being busy sends it again
// before it expires.
type PutBusyRequest struct {
	Reason     string   `json:"reason" enum:"call,meeting,screenshare,dnd"`
	Devices    []string `json:"devices,omitempty" minItems:"1" maxItems:"64" uniqueItems:"true" doc:"device ids; omitted or null: every device"`
	TTLSeconds int      `json:"ttlSeconds" minimum:"1" maximum:"86400" doc:"seconds until the signal expires, at most a day"`
}

// BusySignal is one signal in force. Devices null is every device; ExpiresAt
// null is a Home Assistant one, which lasts while its entity's state holds.
type BusySignal struct {
	Source    string     `json:"source"`
	Reason    string     `json:"reason" enum:"call,meeting,screenshare,dnd"`
	Devices   []string   `json:"devices" nullable:"true"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// BusyDevice is a device busy now, with the reason that wins.
type BusyDevice struct {
	DeviceID string `json:"deviceId"`
	Reason   string `json:"reason" enum:"call,meeting,screenshare,dnd"`
}

// BusyView is busy mode as it stands: every signal in force and the devices
// they make busy.
type BusyView struct {
	Signals []BusySignal `json:"signals" nullable:"false"`
	Devices []BusyDevice `json:"devices" nullable:"false"`
}

type noIn struct{}

type busyOut struct{ Body BusyView }

type sourceIn struct {
	Source string `path:"source" maxLength:"64"`
}

type putIn struct {
	Source string `path:"source" maxLength:"64"`
	api.RawJSON
}

type signalOut struct{ Body BusySignal }

var docs = map[string]api.Doc{
	"get-busy": {Summary: "Get the busy signals in force and the devices they make busy", Scope: auth.ScopePresence},
	"put-busy": {
		Summary: "Set a source's busy signal", Body: PutBusyRequest{}, BodyRequired: true, Scope: auth.ScopePresence,
		BodyNote: "replaces the source's signal; a source starting ha: is Home Assistant's and refused",
	},
	"delete-busy": {Summary: "Clear a source's busy signal", Scope: auth.ScopePresence},
}

// RegisterAdmin serves GET /busy, PUT /busy/{source} and DELETE
// /busy/{source}. Error bodies carry codes only.
func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "get-busy", Method: http.MethodGet, Path: "/busy", Tags: []string{tag},
	}, func(ctx context.Context, _ *noIn) (*busyOut, error) {
		states, err := s.States(ctx)
		if err != nil {
			return nil, s.failed("list", err)
		}
		out := &busyOut{Body: BusyView{Signals: []BusySignal{}, Devices: []BusyDevice{}}}
		for _, sig := range s.Signals() {
			out.Body.Signals = append(out.Body.Signals, toJSON(sig))
		}
		for _, st := range states {
			out.Body.Devices = append(out.Body.Devices, BusyDevice(st))
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-busy", Method: http.MethodPut, Path: "/busy/{source}", Tags: []string{tag},
	}, func(ctx context.Context, in *putIn) (*signalOut, error) {
		var body struct {
			Reason     string    `json:"reason"`
			Devices    *[]string `json:"devices"`
			TTLSeconds int       `json:"ttlSeconds"`
		}
		if !decode(in.Bytes(), &body) {
			return nil, invalid("")
		}
		switch {
		case !sourceRe.MatchString(in.Source) && !strings.HasPrefix(in.Source, haPrefix):
			return nil, invalid("source")
		case !slices.Contains(Reasons, body.Reason):
			return nil, invalid("reason")
		case body.TTLSeconds < 1 || body.TTLSeconds > maxTTLSeconds:
			return nil, invalid("ttlSeconds")
		}
		var devices []string
		if body.Devices != nil {
			if devices = *body.Devices; devices == nil {
				devices = []string{}
			}
		}
		at, err := s.Put(ctx, in.Source, body.Reason, devices, time.Duration(body.TTLSeconds)*time.Second)
		if err != nil {
			return nil, s.refused(err, "devices")
		}
		sig := Signal{Source: in.Source, Reason: body.Reason, ExpiresAt: at}
		for _, cur := range s.Signals() {
			if cur.Source == in.Source {
				sig = cur
			}
		}
		return &signalOut{Body: toJSON(sig)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-busy", Method: http.MethodDelete, Path: "/busy/{source}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *sourceIn) (*struct{}, error) {
		switch err := s.Delete(ctx, in.Source); {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, s.refused(err, "source")
		}
		return nil, nil
	})
}

func toJSON(sig Signal) BusySignal {
	out := BusySignal{Source: sig.Source, Reason: sig.Reason, Devices: sig.Devices}
	if !sig.ExpiresAt.IsZero() {
		at := sig.ExpiresAt.UTC()
		out.ExpiresAt = &at
	}
	return out
}

func invalid(path string) error { return api.Error(http.StatusUnprocessableEntity, "invalid", path) }

// refused is a refused signal: a Home Assistant source, or the field named
// invalid.
func (s *Service) refused(err error, path string) error {
	switch {
	case errors.Is(err, ErrReserved):
		return api.Error(http.StatusUnprocessableEntity, "source_reserved", "source")
	case errors.Is(err, ErrInvalid):
		return invalid(path)
	}
	return s.failed("put", err)
}

func (s *Service) failed(op string, err error) error {
	if errors.Is(err, context.Canceled) {
		return api.Error(499, "canceled")
	}
	s.d.Log.Error("busy_failed", "op", op)
	return api.Error(http.StatusInternalServerError, "internal")
}

// decode reads a strict JSON body: unknown fields, wrong types and trailing
// data are invalid.
func decode(body []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	var rest json.RawMessage
	return dec.Decode(&rest) == io.EOF
}
