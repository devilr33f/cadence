package deviceops

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/pkg/opsplan"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const tag = "ops"

// UploadRoute is the artifact upload as a plain admin route: a body of bytes (a tarball or an
// apk) with its board, kind and label in the query, 512 MiB at most. A huma operation would
// document the body as an object; bytes are what this is.
func (s *Service) UploadRoute() api.PlainRoute {
	return api.PlainRoute{
		Op: "upload-device-artifact", Method: http.MethodPost, Path: "/device-artifacts",
		Handler: func(c fiber.Ctx) error {
			board, kind := c.Query("board"), c.Query("kind")
			label, version := c.Query("label"), c.Query("version")
			if board == "" || kind == "" {
				return c.Status(http.StatusBadRequest).JSON(failBody{"board_and_kind_required"})
			}
			data := c.Body()
			view, err := s.Upload(c.Context(), board, kind, label, version, data)
			if err != nil {
				return uploadFailure(c, err)
			}
			return c.JSON(view)
		},
	}
}

type failBody struct {
	Code string `json:"code"`
}

func uploadFailure(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, ErrBadArtifact):
		return c.Status(http.StatusUnprocessableEntity).JSON(failBody{"bad_artifact"})
	case errors.Is(err, ErrUnknownArtifact):
		return c.Status(http.StatusNotFound).JSON(failBody{"not_found"})
	default:
		return c.Status(http.StatusInternalServerError).JSON(failBody{"internal"})
	}
}

func opFailure(s *Service, err error) error {
	switch {
	case errors.Is(err, ErrUnknownArtifact):
		return api.Error(http.StatusNotFound, "unknown_artifact")
	case errors.Is(err, ErrUnknownRun):
		return api.Error(http.StatusNotFound, "unknown_run")
	case errors.Is(err, ErrUnknownDevice):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrRunActive):
		return api.Error(http.StatusConflict, "run_active")
	case errors.Is(err, ErrBoardMismatch):
		return api.Error(http.StatusConflict, "board_mismatch")
	case errors.Is(err, ErrTransportRefused):
		return api.Error(http.StatusConflict, "transport_refused")
	case errors.Is(err, ErrNoTransport):
		return api.Error(http.StatusConflict, "no_transport")
	case errors.Is(err, ErrBadArtifact):
		return api.Error(http.StatusUnprocessableEntity, "bad_artifact")
	case errors.Is(err, ErrBadAccess):
		return api.Error(http.StatusUnprocessableEntity, "bad_access")
	}
	s.log.Error("ops_api_failed")
	return api.Error(http.StatusInternalServerError, "internal")
}

type listArtifactsIn struct {
	Board string `query:"board" required:"true"`
}

type artifactsOut struct{ Body []ArtifactView }

type artifactIn struct {
	ID string `path:"id"`
}

// RunRequest starts an update.
type RunRequest struct {
	ArtifactID string `json:"artifactId" required:"true" doc:"the artifact to install"`
	// auto picks the daemon's own run when the device is linked and able, else adb, else ssh
	Transport string `json:"transport" enum:"auto,link,adb,ssh" default:"auto"`
}

type runIn struct {
	ID   string `path:"id"`
	Body RunRequest
}

type runOut struct{ Body RunView }

type runsIn struct {
	ID string `path:"id"`
}

type runsOut struct{ Body []RunView }

type runOnlyIn struct {
	ID string `path:"runId"`
}

// AccessRequest stores one way the server reaches a device out of band.
type AccessRequest struct {
	// adb: host:port of its adbd on the lan; ssh: [user@]host[:port]
	Addr string `json:"addr" doc:"adb wants host:port, ssh wants [user@]host[:port]; empty removes the row"`
	// the ssh private key (openssh pem); sealed with the master key before it is stored, never
	// answered back. adb needs none
	SSHKey string `json:"sshKey,omitempty"`
	// the host key pin; empty pins what the first connection shows
	SSHHostKey string `json:"sshHostKey,omitempty"`
}

type accessIn struct {
	ID   string `path:"id"`
	Kind string `path:"kind" enum:"adb,ssh"`
	Body AccessRequest
}

type accessOut struct{ Body AccessView }

type accessListOut struct{ Body []AccessView }

type accessDeleteIn struct {
	ID   string `path:"id"`
	Kind string `path:"kind" enum:"adb,ssh"`
}

type accessProbeIn struct {
	ID   string `path:"id"`
	Kind string `path:"kind" enum:"adb,ssh"`
}

// RegisterAdmin mounts the owner's update api; every operation is admin scope by default.
func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "list-device-artifacts", Method: http.MethodGet, Path: "/device-artifacts", Tags: []string{tag},
	}, func(ctx context.Context, in *listArtifactsIn) (*artifactsOut, error) {
		views, err := s.List(ctx, in.Board)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &artifactsOut{Body: views}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-device-artifact", Method: http.MethodDelete, Path: "/device-artifacts/{id}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *artifactIn) (*struct{}, error) {
		if err := s.Delete(ctx, in.ID); err != nil {
			return nil, opFailure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "start-device-ops", Method: http.MethodPost, Path: "/devices/{id}/ops",
		Tags: []string{tag},
	}, func(ctx context.Context, in *runIn) (*runOut, error) {
		v, err := s.Start(ctx, in.ID, in.Body.ArtifactID, in.Body.Transport)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &runOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "list-device-ops", Method: http.MethodGet, Path: "/devices/{id}/ops", Tags: []string{tag},
	}, func(ctx context.Context, in *runsIn) (*runsOut, error) {
		v, err := s.Runs(ctx, in.ID)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &runsOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-device-ops-run", Method: http.MethodGet, Path: "/ops/{runId}", Tags: []string{tag},
	}, func(ctx context.Context, in *runOnlyIn) (*runOut, error) {
		v, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &runOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "rollback-device-ops", Method: http.MethodPost, Path: "/ops/{runId}/rollback",
		Tags: []string{tag},
	}, func(ctx context.Context, in *runOnlyIn) (*runOut, error) {
		v, err := s.Rollback(ctx, in.ID)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &runOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-device-access", Method: http.MethodPut, Path: "/devices/{id}/access/{kind}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *accessIn) (*struct{}, error) {
		if err := s.SetAccess(ctx, in.ID, in.Kind, in.Body.Addr, in.Body.SSHKey, in.Body.SSHHostKey); err != nil {
			return nil, opFailure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-device-access", Method: http.MethodDelete, Path: "/devices/{id}/access/{kind}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *accessDeleteIn) (*struct{}, error) {
		if err := s.DeleteAccess(ctx, in.ID, in.Kind); err != nil {
			return nil, opFailure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "list-device-access", Method: http.MethodGet, Path: "/devices/{id}/access", Tags: []string{tag},
	}, func(ctx context.Context, in *runsIn) (*accessListOut, error) {
		rows, err := s.Access(ctx, in.ID)
		if err != nil {
			return nil, opFailure(s, err)
		}
		return &accessListOut{Body: rows}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "probe-device-access", Method: http.MethodPost, Path: "/devices/{id}/access/{kind}/probe",
		Tags: []string{tag},
	}, func(ctx context.Context, in *accessProbeIn) (*accessOut, error) {
		if err := s.ProbeAccess(ctx, in.ID, in.Kind); err != nil {
			return nil, opFailure(s, err)
		}
		rows, err := s.Access(ctx, in.ID)
		if err != nil {
			return nil, opFailure(s, err)
		}
		for _, r := range rows {
			if r.Kind == in.Kind {
				return &accessOut{Body: r}, nil
			}
		}
		return &accessOut{}, nil
	})
}

var docs = map[string]api.Doc{
	"list-device-artifacts":  {Summary: "List a board's stored build artifacts, newest first"},
	"delete-device-artifact": {Summary: "Delete a stored artifact", Scope: auth.ScopeAdmin},
	"start-device-ops": {
		Summary: "Start a device update: installs an artifact over the daemon's own run (link), adb or ssh, one run at a time; agents never call this",
		Body:    RunRequest{}, BodyRequired: true,
	},
	"list-device-ops":      {Summary: "A device's update runs, newest first, each with its steps"},
	"get-device-ops-run":   {Summary: "One update run with its steps"},
	"rollback-device-ops":  {Summary: "Install the artifact the device ran before this run's (an owner's undo)"},
	"put-device-access":    {Summary: "Store one way the server reaches a device out of band (adb on the lan, ssh to its host); the ssh key is sealed, never answered", Body: AccessRequest{}, BodyRequired: true},
	"list-device-access":   {Summary: "A device's out-of-band access rows (never the sealed ssh key)"},
	"delete-device-access": {Summary: "Remove a device's access row"},
	"probe-device-access":  {Summary: "Try a device's access row and record how it went"},
}

const authWait = 5 * time.Second

// MountDevice serves the daemon's two routes on the device api (/v2): its resolved plan and the
// plan's files, both with the device's bearer token (the ring-sound pattern; the token never in
// a url). A run answers only the device it belongs to.
func (s *Service) MountDevice(r fiber.Router, authDevice func(ctx context.Context, token string) (string, error)) {
	r.Get("/device-plan/:runId", func(c fiber.Ctx) error {
		dev, ok := bearer(c, authDevice)
		if !ok {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		plan, err := s.planFor(c.Context(), dev, c.Params("runId"))
		switch {
		case errors.Is(err, ErrUnknownRun):
			return c.SendStatus(fiber.StatusNotFound)
		case errors.Is(err, ErrUnknownDevice):
			return c.SendStatus(fiber.StatusForbidden)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		c.Set(fiber.HeaderContentType, "application/json")
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		return c.Send(plan)
	})
	whole := func(c fiber.Ctx) error {
		dev, ok := bearer(c, authDevice)
		if !ok {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		b, sha, err := s.artifactFor(c.Context(), dev, c.Params("runId"), c.Params("*"))
		switch {
		case errors.Is(err, ErrUnknownRun), errors.Is(err, ErrUnknownArtifact):
			return c.SendStatus(fiber.StatusNotFound)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		s.log.Info("ops_artifact_served", "device_id", dev, "bytes", len(b))
		c.Set(fiber.HeaderContentType, "application/octet-stream")
		c.Set(fiber.HeaderETag, `"`+sha+`"`)
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		return c.Send(b)
	}
	r.Get("/device-artifact/:runId", whole)
	r.Get("/device-artifact/:runId/*", whole)
}

func bearer(c fiber.Ctx, authDevice func(ctx context.Context, token string) (string, error)) (string, bool) {
	token, has := cutBearer(c.Get(fiber.HeaderAuthorization))
	if !has || token == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(c.Context(), authWait)
	defer cancel()
	dev, err := authDevice(ctx, token)
	if err != nil {
		return "", false
	}
	return dev, true
}

func cutBearer(h string) (string, bool) {
	if len(h) > 7 && h[:7] == "Bearer " {
		return h[7:], true
	}
	return "", false
}

// planFor answers the run's resolved plan, for its device alone.
func (s *Service) planFor(ctx context.Context, deviceID, runID string) ([]byte, error) {
	v, err := s.runView(ctx, runID)
	if err != nil {
		return nil, err
	}
	if v.DeviceID != deviceID {
		return nil, ErrUnknownRun
	}
	row, err := s.planBytes(ctx, runID)
	if err != nil {
		return nil, err
	}
	if _, err := opsplan.Unmarshal(row); err != nil {
		return nil, err
	}
	return row, nil
}

// artifactFor answers one file the run's plan fetches: a member of a tar artifact or its whole
// blob, so a device cannot pull anything else the store holds.
func (s *Service) artifactFor(ctx context.Context, deviceID, runID, name string) ([]byte, string, error) {
	v, err := s.runView(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	if v.DeviceID != deviceID {
		return nil, "", ErrUnknownRun
	}
	row, err := s.planBytes(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	plan, err := opsplan.Unmarshal(row)
	if err != nil {
		return nil, "", err
	}
	src := "artifact:" + name
	for _, st := range plan.Steps {
		if st.Fetch != nil && st.Fetch.Src == src {
			b, err := s.runArtifact(ctx, v, name)
			return b, st.Fetch.SHA256, err
		}
	}
	if name == "" {
		// the whole blob (an apk, the rpi tarball) travels as "artifact:" with an empty rest
		for _, st := range plan.Steps {
			if st.Fetch != nil && st.Fetch.Src == "artifact:" {
				b, err := s.runArtifact(ctx, v, "")
				return b, st.Fetch.SHA256, err
			}
		}
	}
	return nil, "", ErrUnknownArtifact
}

func (s *Service) runArtifact(ctx context.Context, v RunView, name string) ([]byte, error) {
	if v.ArtifactID == "" {
		return nil, ErrUnknownArtifact
	}
	a, err := s.loadArtifact(ctx, v.ArtifactID)
	if err != nil {
		return nil, err
	}
	return a.open("artifact:" + name)
}

// planBytes answers the run's stored plan json.
func (s *Service) planBytes(ctx context.Context, runID string) ([]byte, error) {
	uid, ok := uuid(runID)
	if !ok {
		return nil, ErrUnknownRun
	}
	r, err := s.q.GetOpsRun(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownRun
	}
	if err != nil {
		return nil, err
	}
	return r.Plan, nil
}
