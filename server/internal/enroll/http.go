package enroll

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

const tag = "enroll"

type createIn struct {
	Body struct {
		Name  string `json:"name" minLength:"1" doc:"trimmed of white space, then 1–64 characters with no control or format character and no line or paragraph separator; no other device's name, in any case"`
		Model string `json:"model" minLength:"1"`
	}
}

type createOut struct {
	Body struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Model         string    `json:"model"`
		Status        string    `json:"status"`
		Code          string    `json:"code"`
		CodeExpiresAt time.Time `json:"codeExpiresAt"`
	}
}

type deviceView struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Model      string            `json:"model"`
	Status     string            `json:"status"`
	Room       string            `json:"room" doc:"the room the device is in, the owner's name for it; empty for none"`
	Online     bool              `json:"online" doc:"linked to the server now"`
	LastSeenAt *time.Time        `json:"lastSeenAt"`
	Group      *devices.GroupRef `json:"group,omitempty" doc:"the multiroom group the device is in; none when in none"`
}

type listOut struct{ Body []deviceView }

type boardView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type boardsOut struct{ Body []boardView }

// RegisterAdmin adds the enrol and list routes; online says whether a device is
// linked now (the hub's Online).
func RegisterAdmin(a huma.API, s *Service, online func(deviceID string) bool) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "create-device", Method: http.MethodPost, Path: "/devices", Tags: []string{tag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*createOut, error) {
		d, code, err := s.CreateDevice(ctx, in.Body.Name, in.Body.Model)
		switch {
		case errors.Is(err, ErrUnknownModel):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_model")
		case errors.Is(err, ErrNameInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/name")
		case errors.Is(err, ErrNameTaken):
			return nil, api.Error(http.StatusConflict, "name_taken")
		case err != nil:
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &createOut{}
		out.Body.ID, out.Body.Name, out.Body.Model, out.Body.Status = d.ID, d.Name, d.Model, d.Status
		out.Body.Code, out.Body.CodeExpiresAt = code, d.ExpiresAt
		return out, nil
	})
	huma.Register(a, huma.Operation{OperationID: "list-devices", Method: http.MethodGet, Path: "/devices", Tags: []string{tag}},
		func(ctx context.Context, _ *struct{}) (*listOut, error) {
			rows, err := s.List(ctx)
			if err != nil {
				return nil, api.Error(http.StatusInternalServerError, "internal")
			}
			out := &listOut{Body: make([]deviceView, 0, len(rows))}
			// a failed read of the groups leaves them out
			var refs map[string]devices.GroupRef
			if s.groupRefs != nil {
				refs, _ = s.groupRefs(ctx)
			}
			for _, r := range rows {
				v := deviceView{
					ID: r.ID, Name: r.Name, Model: r.Model, Status: r.Status, Room: r.Room, Online: online(r.ID), LastSeenAt: r.LastSeenAt,
				}
				if g, ok := refs[r.ID]; ok {
					v.Group = &g
				}
				out.Body = append(out.Body, v)
			}
			return out, nil
		})
	huma.Register(a, huma.Operation{OperationID: "list-boards", Method: http.MethodGet, Path: "/boards", Tags: []string{tag}},
		func(context.Context, *struct{}) (*boardsOut, error) {
			out := &boardsOut{Body: make([]boardView, 0, len(Boards))}
			for _, b := range Boards {
				out.Body = append(out.Body, boardView(b))
			}
			return out, nil
		})
}

type enrollReq struct {
	Code        string `json:"code"`
	Fingerprint struct {
		Board string `json:"board"`
	} `json:"fingerprint"`
}

const refusedBody = `{"error":"enroll_refused"}`

func MountDevice(r fiber.Router, s *Service) {
	r.Post("/enroll", func(c fiber.Ctx) error {
		refuse := func() error {
			c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			return c.Status(fiber.StatusForbidden).SendString(refusedBody)
		}
		var req enrollReq
		if err := c.Bind().JSON(&req); err != nil {
			return refuse()
		}
		id, token, err := s.Enroll(c.Context(), req.Code, Fingerprint{Board: req.Fingerprint.Board})
		switch {
		case errors.Is(err, ErrRefused):
			return refuse()
		case err != nil:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal"})
		}
		return c.JSON(fiber.Map{"deviceId": id, "token": token})
	})
}
