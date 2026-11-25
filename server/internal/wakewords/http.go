package wakewords

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/pkg/kws"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const tag = "wakewords"

// Tag describes the operations' group in the OpenAPI document.
const TagDescription = "Custom wake words (wake.custom): models uploaded ready or trained from the owner's takes, " +
	"the training's negatives, and the model a device's config names."

const authWait = 5 * time.Second

func failure(s *Service, err error) error {
	var de *DatasetError
	switch {
	case errors.As(err, &de):
		return api.ErrorWithDetail(http.StatusUnprocessableEntity, "invalid_dataset", de.Code)
	case errors.Is(err, ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrInvalidModel):
		return api.Error(http.StatusUnprocessableEntity, "invalid_model")
	case errors.Is(err, ErrInvalidWord):
		return api.Error(http.StatusUnprocessableEntity, "invalid_word")
	case errors.Is(err, ErrNoDataset):
		return api.Error(http.StatusConflict, "no_dataset")
	case errors.Is(err, ErrTraining):
		return api.Error(http.StatusConflict, "training")
	case errors.Is(err, ErrFetching):
		return api.Error(http.StatusConflict, "fetching")
	case errors.Is(err, ErrNoPath):
		return api.Error(http.StatusConflict, "negatives_unconfigured")
	}
	s.d.Log.Error("wakeword_api_failed")
	return api.Error(http.StatusInternalServerError, "internal")
}

type idIn struct {
	ID string `path:"id" format:"uuid"`
}

type viewOut struct{ Body WakeWordView }

type listOut struct{ Body []WakeWordView }

type uploadModelIn struct {
	// the label; empty takes the model's own word
	Word    string `query:"word" maxLength:"64"`
	RawBody []byte `contentType:"application/json"`
}

type trainIn struct {
	Word string `query:"word" required:"true" minLength:"1" maxLength:"64"`
	// store the dataset for a retrain; else it is dropped once the training ends
	Keep    bool   `query:"keep"`
	RawBody []byte `contentType:"application/zip"`
}

// WakeWordRename sets a word's label.
type WakeWordRename struct {
	Word string `json:"word" minLength:"1" maxLength:"64"`
}

type renameIn struct {
	ID   string `path:"id" format:"uuid"`
	Body WakeWordRename
}

type modelOut struct {
	ContentType string `header:"Content-Type"`
	ETag        string `header:"ETag"`
	Body        []byte
}

type negativesOut struct{ Body WakeWordNegatives }

func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{OperationID: "list-wake-words", Method: http.MethodGet, Path: "/wakewords",
		Tags: []string{tag}}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		v, err := s.List(ctx)
		if err != nil {
			return nil, failure(s, err)
		}
		return &listOut{Body: v}, nil
	})
	// before /wakewords/{id}, which fiber would match first
	huma.Register(a, huma.Operation{OperationID: "get-wake-word-negatives", Method: http.MethodGet,
		Path: "/wakewords/negatives", Tags: []string{tag}}, func(context.Context, *struct{}) (*negativesOut, error) {
		return &negativesOut{Body: s.Negatives()}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "fetch-wake-word-negatives", Method: http.MethodPost,
		Path: "/wakewords/negatives/fetch", Tags: []string{tag}, DefaultStatus: http.StatusAccepted,
	}, func(context.Context, *struct{}) (*negativesOut, error) {
		if err := s.FetchNegatives(); err != nil {
			return nil, failure(s, err)
		}
		return &negativesOut{Body: s.Negatives()}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "get-wake-word", Method: http.MethodGet, Path: "/wakewords/{id}",
		Tags: []string{tag}}, func(ctx context.Context, in *idIn) (*viewOut, error) {
		v, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, failure(s, err)
		}
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "upload-wake-word-model", Method: http.MethodPost,
		Path: "/wakewords/models", Tags: []string{tag}, DefaultStatus: http.StatusCreated, MaxBodyBytes: kws.MaxModelBytes,
	}, func(ctx context.Context, in *uploadModelIn) (*viewOut, error) {
		v, err := s.UploadModel(ctx, in.Word, in.RawBody)
		if err != nil {
			return nil, failure(s, err)
		}
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "train-wake-word", Method: http.MethodPost, Path: "/wakewords/datasets",
		Tags: []string{tag}, DefaultStatus: http.StatusAccepted, MaxBodyBytes: kws.MaxDatasetBytes,
		BodyReadTimeout: 5 * time.Minute,
	}, func(ctx context.Context, in *trainIn) (*viewOut, error) {
		v, err := s.Train(ctx, in.Word, in.RawBody, in.Keep)
		if err != nil {
			return nil, failure(s, err)
		}
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "retrain-wake-word", Method: http.MethodPost,
		Path: "/wakewords/{id}/retrain", Tags: []string{tag}, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *idIn) (*viewOut, error) {
		v, err := s.Retrain(ctx, in.ID)
		if err != nil {
			return nil, failure(s, err)
		}
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "rename-wake-word", Method: http.MethodPatch, Path: "/wakewords/{id}",
		Tags: []string{tag}}, func(ctx context.Context, in *renameIn) (*viewOut, error) {
		v, err := s.Rename(ctx, in.ID, in.Body.Word)
		if err != nil {
			return nil, failure(s, err)
		}
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "delete-wake-word", Method: http.MethodDelete, Path: "/wakewords/{id}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := s.Delete(ctx, in.ID); err != nil {
			return nil, failure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "delete-wake-word-dataset", Method: http.MethodDelete,
		Path: "/wakewords/{id}/dataset", Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := s.DeleteDataset(ctx, in.ID); err != nil {
			return nil, failure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{OperationID: "get-wake-word-model", Method: http.MethodGet,
		Path: "/wakewords/{id}/model", Tags: []string{tag}}, func(ctx context.Context, in *idIn) (*modelOut, error) {
		b, sha, err := s.Model(ctx, in.ID)
		if err != nil {
			return nil, failure(s, err)
		}
		return &modelOut{ContentType: "application/json", ETag: `"` + sha + `"`, Body: b}, nil
	})
}

var docs = map[string]api.Doc{
	"list-wake-words": {Summary: "The custom wake words, newest first, a training's progress with it"},
	"get-wake-word":   {Summary: "One custom wake word"},
	"upload-wake-word-model": {Summary: "Store a ready model file (pkg/kws's JSON, at most 1 MiB); the word " +
		"query names it, else the model's own word does"},
	"train-wake-word": {Summary: "Train a word from a dataset zip: takes/*.wav (3 or more, 6 to 15 is best), an " +
		"ambient*.wav of the room and an optional speech*.wav of the owner talking; any rate or channels, at most " +
		"256 MiB. Answers at once with the word training; progress comes on the live feed's wakewords topic. The " +
		"dataset is dropped after the training unless keep is set"},
	"retrain-wake-word":        {Summary: "Train a word again from its kept dataset"},
	"rename-wake-word":         {Summary: "Rename a word", Body: WakeWordRename{}, BodyRequired: true},
	"delete-wake-word":         {Summary: "Delete a word, cancelling its training"},
	"delete-wake-word-dataset": {Summary: "Delete a word's kept dataset"},
	"get-wake-word-model":      {Summary: "A ready word's model file", Reply: kws.Model{}},
	"get-wake-word-negatives": {Summary: "The training's negative features: whether CADENCE_WAKEWORD_NEGATIVES " +
		"names a file, whether it holds them, and a fetch in progress"},
	"fetch-wake-word-negatives": {Summary: "Fetch a sample of openWakeWord's negative features (CC BY-NC-SA 4.0, " +
		"about 25 MiB from huggingface) into the negatives file; progress on the wakewords topic", Scope: auth.ScopeAdmin},
}

// MountDevice serves GET /wakeword/{id} on the device api: a ready model's file to any enrolled device, with its
// bearer token (never in the url). 401 for an unknown token, 404 for an id with no ready model.
func (s *Service) MountDevice(r fiber.Router, authDevice func(ctx context.Context, token string) (string, error)) {
	r.Get("/wakeword/:id", func(c fiber.Ctx) error {
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || token == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		ctx, cancel := context.WithTimeout(c.Context(), authWait)
		dev, err := authDevice(ctx, token)
		cancel()
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			return c.SendStatus(fiber.StatusUnauthorized)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		b, sha, err := s.Model(c.Context(), c.Params("id"))
		switch {
		case errors.Is(err, ErrNotFound):
			s.d.Log.Info("wakeword_refused", "device_id", dev, "code", "not_found")
			return c.SendStatus(fiber.StatusNotFound)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		s.d.Log.Info("wakeword_served", "device_id", dev, "bytes", len(b))
		c.Set(fiber.HeaderContentType, "application/json")
		c.Set(fiber.HeaderETag, `"`+sha+`"`)
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		return c.Send(b)
	})
}
