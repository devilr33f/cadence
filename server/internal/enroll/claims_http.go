package enroll

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	claimTag = "claims"
	// a poll waits this long by default, and at most claimMaxWait
	claimWait    = 25 * time.Second
	claimMaxWait = 30 * time.Second
	// the screen polls again this soon after an answer
	claimPollAfter = time.Second
)

// ClaimView is an open claim as the dashboard lists it.
type ClaimView struct {
	Code      string    `json:"code" doc:"6 characters, shown on the screen as two groups of three"`
	Board     string    `json:"board"`
	Source    string    `json:"source" doc:"the address the claim came from"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type claimsOut struct{ Body []ClaimView }

type claimCodeIn struct {
	Code string `path:"code" maxLength:"16"`
}

// ApproveClaimRequest names the screen and its room.
type ApproveClaimRequest struct {
	Name string `json:"name" minLength:"1" doc:"the device's name, as create-device takes it: trimmed of white space, then 1–64 characters with no control or format character and no line or paragraph separator; no other device's name, in any case"`
	Room string `json:"room,omitempty" maxLength:"256" doc:"the device's room, as set-device-room takes it; empty for none"`
}

type approveIn struct {
	Code string `path:"code" maxLength:"16"`
	Body ApproveClaimRequest
}

type approveOut struct {
	Body struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Model  string `json:"model"`
		Room   string `json:"room"`
		Status string `json:"status" doc:"pending until the screen's next poll takes its token"`
	}
}

// RegisterClaimsAdmin adds the claim routes the dashboard approves screens with.
func RegisterClaimsAdmin(a huma.API, s *ClaimService) {
	huma.Register(a, huma.Operation{
		OperationID: "list-claims", Method: http.MethodGet, Path: "/claims", Tags: []string{claimTag},
		Summary: "List the open claims of screens waiting to be approved",
	}, func(ctx context.Context, _ *struct{}) (*claimsOut, error) {
		cs, err := s.Open(ctx)
		if err != nil {
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &claimsOut{Body: make([]ClaimView, len(cs))}
		for i, c := range cs {
			out.Body[i] = ClaimView{Code: c.Code, Board: c.Board, Source: c.Source, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-claim", Method: http.MethodGet, Path: "/claims/{code}", Tags: []string{claimTag},
		Summary:     "Get an open claim by its code",
		Description: "404 not_found for a code no open claim has: unknown, expired, approved or declined.",
	}, func(ctx context.Context, in *claimCodeIn) (*struct{ Body ClaimView }, error) {
		norm, ok := NormalizeClaimCode(in.Code)
		if !ok {
			return nil, api.Error(http.StatusNotFound, "not_found")
		}
		cs, err := s.Open(ctx)
		if err != nil {
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		for _, c := range cs {
			if c.Code == norm {
				return &struct{ Body ClaimView }{Body: ClaimView{Code: c.Code, Board: c.Board, Source: c.Source,
					CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}}, nil
			}
		}
		return nil, api.Error(http.StatusNotFound, "not_found")
	})
	huma.Register(a, huma.Operation{
		OperationID: "approve-claim", Method: http.MethodPost, Path: "/claims/{code}/approve", Tags: []string{claimTag},
		DefaultStatus: http.StatusCreated,
		Summary:       "Approve a screen's claim: create its device with a name and a room",
		Description: "The device is created pending and enrolled by the screen's next poll, which takes its token " +
			"(GET /v2/claim/{id}). 404 not_found for a code no open claim has; 409 name_taken; 422 invalid.",
	}, func(ctx context.Context, in *approveIn) (*approveOut, error) {
		d, err := s.Approve(ctx, in.Code, Approval{Name: in.Body.Name, Room: in.Body.Room})
		switch {
		case errors.Is(err, ErrClaimNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrNameInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/name")
		case errors.Is(err, ErrRoomInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/room")
		case errors.Is(err, ErrNameTaken):
			return nil, api.Error(http.StatusConflict, "name_taken")
		case err != nil:
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &approveOut{}
		out.Body.ID, out.Body.Name, out.Body.Model, out.Body.Room, out.Body.Status = d.ID, d.Name, d.Model, d.Room, d.Status
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "decline-claim", Method: http.MethodPost, Path: "/claims/{code}/decline", Tags: []string{claimTag},
		DefaultStatus: http.StatusNoContent,
		Summary:       "Decline a screen's claim",
		Description:   "The screen's poll answers declined, and it shows a new code. 404 not_found for a code no open claim has.",
	}, func(ctx context.Context, in *claimCodeIn) (*struct{}, error) {
		err := s.Decline(ctx, in.Code)
		switch {
		case errors.Is(err, ErrClaimNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &struct{}{}, nil
	})
}

type claimReq struct {
	Board string `json:"board"`
}

// ClaimPath is the dashboard's page for a claim, which the screen's qr encodes after the origin.
const ClaimPath = "/claims/"

// MountClaims serves the screen's side of the claim: POST /v2/claim and GET /v2/claim/{id}. origin is the dashboard's
// (CADENCE_PUBLIC_ORIGIN); empty takes the request's own.
func MountClaims(r fiber.Router, s *ClaimService, origin string) {
	fail := func(c fiber.Ctx, status int, code string) error {
		return c.Status(status).JSON(fiber.Map{"error": code})
	}
	r.Post("/claim", func(c fiber.Ctx) error {
		var req claimReq
		if err := c.Bind().JSON(&req); err != nil {
			return fail(c, fiber.StatusBadRequest, "invalid")
		}
		cl, err := s.Start(c.Context(), req.Board, c.IP())
		switch {
		case errors.Is(err, ErrUnknownBoard):
			return fail(c, fiber.StatusBadRequest, "unknown_board")
		case errors.Is(err, ErrClaimRateLimited):
			c.Set(fiber.HeaderRetryAfter, "60")
			return fail(c, fiber.StatusTooManyRequests, "rate_limited")
		case err != nil:
			return fail(c, fiber.StatusInternalServerError, "internal")
		}
		base := origin
		if base == "" {
			base = c.BaseURL()
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"id": cl.ID, "code": cl.Code, "url": strings.TrimRight(base, "/") + ClaimPath + cl.Code,
			"expiresAt": cl.ExpiresAt.UTC(), "pollAfterMs": claimPollAfter.Milliseconds(),
		})
	})
	r.Get("/claim/:id", func(c fiber.Ctx) error {
		id := c.Params("id")
		if !strings.HasPrefix(id, claimIDPrefix) || len(id) > 128 {
			return fail(c, fiber.StatusNotFound, "not_found")
		}
		wait := claimWait
		if q := c.Query("wait"); q != "" {
			d, err := time.ParseDuration(q)
			if err != nil || d < 0 {
				return fail(c, fiber.StatusBadRequest, "invalid")
			}
			wait = min(d, claimMaxWait)
		}
		ctx, cancel := context.WithTimeout(c.Context(), wait)
		defer cancel()
		res, err := s.Wait(ctx, id)
		switch {
		case errors.Is(err, ErrClaimNotFound):
			return fail(c, fiber.StatusNotFound, "not_found")
		case err != nil:
			return fail(c, fiber.StatusInternalServerError, "internal")
		}
		c.Set(fiber.HeaderCacheControl, "no-store")
		if res.State == ClaimApproved {
			return c.JSON(fiber.Map{"state": res.State, "deviceId": res.DeviceID, "token": res.Token})
		}
		return c.JSON(fiber.Map{"state": res.State})
	})
}
