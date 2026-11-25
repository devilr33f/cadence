// Package wakewords is the server's side of the custom wake word (wake.custom): the models, stored in Postgres, either
// uploaded ready or trained here by pkg/kws from a dataset of the owner's takes; their training as a background job
// whose progress goes out on the live feed; the negative features training needs, fetched on the owner's ask into the
// file CADENCE_WAKEWORD_NEGATIVES names; and the device route a device fetches a model through with its token.
//
// A dataset is audio: it is held in memory for its training and stored only when the owner asks to keep it (for a
// retrain), until they delete it. Nothing of it, nor the word, reaches a log: lines carry ids, codes, counts and
// timings (rule 8).
package wakewords

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/pkg/kws"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// The live feed's events (the wakewords topic): EventChanged a model added, changed or deleted {id, status, code};
// EventProgress a training's stage {id, stage, fraction}, at most every 2 %; EventNegatives the negatives' fetch
// {state, fraction, code}.
const (
	EventChanged   = "wakeword_changed"
	EventProgress  = "wakeword_progress"
	EventNegatives = "wakeword_negatives"
)

var (
	ErrNotFound     = errors.New("wakewords: not found")
	ErrInvalidModel = errors.New("wakewords: invalid model")
	ErrInvalidWord  = errors.New("wakewords: invalid word")
	ErrNoDataset    = errors.New("wakewords: no dataset kept")
	ErrTraining     = errors.New("wakewords: training")
	ErrNoNegatives  = errors.New("wakewords: no negatives")
	ErrNoPath       = errors.New("wakewords: no negatives path")
	ErrFetching     = errors.New("wakewords: fetching")
)

// DatasetError is a refused dataset, with pkg/kws's code.
type DatasetError = kws.DatasetError

type Deps struct {
	Q   *db.Queries
	Bus events.Publisher
	// the negatives file (CADENCE_WAKEWORD_NEGATIVES); empty: training and fetching are refused
	Negatives string
	// a training's goroutines; 0 is half the cores, at most 4
	Workers int
	// a training's passes; 0 is pkg/kws's default (tests take fewer)
	Epochs int
	// the negatives' source; nil is the real one (huggingface)
	Fetch Fetcher
	Log   *slog.Logger
	Now   func() time.Time
}

type Service struct {
	d Deps

	// one training at a time; the rest wait their turn
	slot chan struct{}

	mu       sync.Mutex
	jobs     map[string]context.CancelFunc
	progress map[string]WakeWordProgress
	fetching *WakeWordNegativesProgress
}

// WakeWordProgress is a training's stage: queued, embed, train or calibrate, and how far it went (0 to 1).
type WakeWordProgress struct {
	Stage    string  `json:"stage" enum:"queued,embed,train,calibrate"`
	Fraction float64 `json:"fraction" minimum:"0" maximum:"1"`
}

func New(d Deps) *Service {
	if d.Workers <= 0 {
		d.Workers = max(1, min(runtime.NumCPU()/2, 4))
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Fetch == nil {
		d.Fetch = huggingFace{}
	}
	return &Service{d: d, slot: make(chan struct{}, 1), jobs: map[string]context.CancelFunc{},
		progress: map[string]WakeWordProgress{}}
}

// Run marks the trainings a previous run left unfinished as failed (interrupted); stop cancels the running ones.
func (s *Service) Run(ctx context.Context) (stop func()) {
	if n, err := s.d.Q.FailInterruptedWakeWords(ctx); err != nil {
		s.d.Log.Warn("wakeword_sweep_failed")
	} else if n > 0 {
		s.d.Log.Info("wakeword_interrupted", "count", n)
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, cancel := range s.jobs {
			cancel()
		}
	}
}

// WakeWordView is a wake word as the admin api shows it: never the model's or the dataset's bytes.
type WakeWordView struct {
	ID     string `json:"id" format:"uuid"`
	Word   string `json:"word"`
	Source string `json:"source" enum:"upload,trained"`
	Status string `json:"status" enum:"training,ready,failed"`
	// why it failed: a pkg/kws code (too_few_takes, no_ambient, no_negatives, ...), interrupted, or internal
	Code   string `json:"code,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// the model's own threshold; a device's sensitivity moves it
	Threshold *float32 `json:"threshold,omitempty"`
	Takes     *int32   `json:"takes,omitempty"`
	// the held-out takes detected at the threshold, 0 to 1
	Recall *float64 `json:"recall,omitempty"`
	// detections per hour on held-out negatives, over evalHours of them
	FalsePerHour *float64          `json:"falsePerHour,omitempty"`
	EvalHours    *float64          `json:"evalHours,omitempty"`
	ModelBytes   int64             `json:"modelBytes"`
	DatasetKept  bool              `json:"datasetKept"`
	Progress     *WakeWordProgress `json:"progress,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	TrainedAt    *time.Time        `json:"trainedAt,omitempty"`
}

func (s *Service) List(ctx context.Context) ([]WakeWordView, error) {
	rows, err := s.d.Q.ListWakeWords(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WakeWordView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.view(db.GetWakeWordInfoRow(r)))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id string) (WakeWordView, error) {
	uid, ok := parseID(id)
	if !ok {
		return WakeWordView{}, ErrNotFound
	}
	r, err := s.d.Q.GetWakeWordInfo(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return WakeWordView{}, ErrNotFound
	}
	if err != nil {
		return WakeWordView{}, err
	}
	return s.view(r), nil
}

func (s *Service) view(r db.GetWakeWordInfoRow) WakeWordView {
	v := WakeWordView{ID: uuidString(r.ID), Word: r.Word, Source: r.Source, Status: r.Status, Code: r.ErrorCode,
		SHA256: r.Sha256.String, ModelBytes: r.ModelSize, DatasetKept: r.DatasetSize > 0, CreatedAt: r.CreatedAt.Time}
	if r.Threshold.Valid {
		v.Threshold = &r.Threshold.Float32
	}
	if r.Takes.Valid {
		v.Takes = &r.Takes.Int32
	}
	if r.Recall.Valid {
		v.Recall = &r.Recall.Float64
	}
	if r.FalsePerHour.Valid {
		v.FalsePerHour = &r.FalsePerHour.Float64
	}
	if r.EvalHours.Valid {
		v.EvalHours = &r.EvalHours.Float64
	}
	if r.TrainedAt.Valid {
		v.TrainedAt = &r.TrainedAt.Time
	}
	if v.Status == "training" {
		s.mu.Lock()
		if p, ok := s.progress[v.ID]; ok {
			v.Progress = &p
		}
		s.mu.Unlock()
	}
	return v
}

func checkWord(word string) (string, error) {
	word = strings.TrimSpace(word)
	if word == "" || utf8.RuneCountInString(word) > 64 || !utf8.ValidString(word) || strings.ContainsAny(word, "\x00\r\n") {
		return "", ErrInvalidWord
	}
	return word, nil
}

// UploadModel stores a ready model file; word empty takes the model's own.
func (s *Service) UploadModel(ctx context.Context, word string, file []byte) (WakeWordView, error) {
	m, err := kws.ParseModel(file)
	if err != nil {
		return WakeWordView{}, ErrInvalidModel
	}
	if word == "" {
		word = m.Word
	}
	if word, err = checkWord(word); err != nil {
		return WakeWordView{}, err
	}
	now := s.d.Now()
	row, err := s.d.Q.InsertWakeWord(ctx, db.InsertWakeWordParams{
		Word: word, Source: "upload", Status: "ready", Model: file, Sha256: text(sum(file)),
		Threshold: pgtype.Float4{Float32: m.Threshold, Valid: true}, Takes: takes(m.Takes),
		Recall: float8(m.Recall, m.EvalHours > 0), FalsePerHour: float8(m.FalsePerHour, m.EvalHours > 0),
		EvalHours: float8(m.EvalHours, m.EvalHours > 0), TrainedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return WakeWordView{}, err
	}
	id := uuidString(row.ID)
	s.d.Log.Info("wakeword_uploaded", "id", id, "bytes", len(file))
	s.changed(id, "ready", "")
	return s.Get(ctx, id)
}

// Train checks a dataset zip, stores the word as training and starts its job; keep stores the dataset too, for a
// retrain. A dataset pkg/kws refuses is a *DatasetError and stores nothing.
func (s *Service) Train(ctx context.Context, word string, dataset []byte, keep bool) (WakeWordView, error) {
	word, err := checkWord(word)
	if err != nil {
		return WakeWordView{}, err
	}
	if s.d.Negatives == "" {
		return WakeWordView{}, ErrNoPath
	}
	en, err := kws.ReadDataset(dataset)
	if err != nil {
		return WakeWordView{}, err
	}
	var kept []byte
	if keep {
		kept = dataset
	}
	row, err := s.d.Q.InsertWakeWord(ctx, db.InsertWakeWordParams{Word: word, Source: "trained", Status: "training",
		Dataset: kept})
	if err != nil {
		return WakeWordView{}, err
	}
	id := uuidString(row.ID)
	s.d.Log.Info("wakeword_training_queued", "id", id, "takes", len(en.Takes), "kept", keep)
	s.start(id, word, en)
	return s.Get(ctx, id)
}

// Retrain trains the word again from its kept dataset.
func (s *Service) Retrain(ctx context.Context, id string) (WakeWordView, error) {
	uid, ok := parseID(id)
	if !ok {
		return WakeWordView{}, ErrNotFound
	}
	if s.d.Negatives == "" {
		return WakeWordView{}, ErrNoPath
	}
	row, err := s.d.Q.GetWakeWordDataset(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.Get(ctx, id); gerr != nil {
			return WakeWordView{}, gerr
		}
		return WakeWordView{}, ErrNoDataset
	}
	if err != nil {
		return WakeWordView{}, err
	}
	en, err := kws.ReadDataset(row.Dataset)
	if err != nil {
		return WakeWordView{}, err
	}
	n, err := s.d.Q.StartWakeWordTraining(ctx, uid)
	if err != nil {
		return WakeWordView{}, err
	}
	if n == 0 {
		return WakeWordView{}, ErrTraining
	}
	s.start(id, row.Word, en)
	return s.Get(ctx, id)
}

func (s *Service) start(id, word string, en *kws.Enrollment) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.jobs[id] = cancel
	s.progress[id] = WakeWordProgress{Stage: "queued"}
	s.mu.Unlock()
	s.changed(id, "training", "")
	go s.train(ctx, id, word, en)
}

func (s *Service) train(ctx context.Context, id, word string, en *kws.Enrollment) {
	defer func() {
		s.mu.Lock()
		if cancel, ok := s.jobs[id]; ok {
			cancel()
		}
		delete(s.jobs, id)
		delete(s.progress, id)
		s.mu.Unlock()
	}()
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.slot }()
	began := s.d.Now()
	fail := func(code string) {
		uid, _ := parseID(id)
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.d.Q.FailWakeWordTraining(dctx, db.FailWakeWordTrainingParams{ErrorCode: code, ID: uid}); err != nil {
			s.d.Log.Warn("wakeword_store_failed", "id", id)
		}
		s.d.Log.Info("wakeword_training_failed", "id", id, "code", code)
		s.changed(id, "failed", code)
	}
	neg, err := s.negatives()
	if err != nil {
		fail("no_negatives")
		return
	}
	var lastStage string
	var lastFrac float64
	m, err := kws.TrainWord(ctx, en, neg, kws.TrainOptions{
		Word: word, Seed: uint64(began.UnixNano()), Workers: s.d.Workers, Epochs: s.d.Epochs, Now: s.d.Now,
		Progress: func(stage string, frac float64) {
			if stage == lastStage && frac-lastFrac < 0.02 && frac < 1 {
				return
			}
			lastStage, lastFrac = stage, frac
			s.mu.Lock()
			s.progress[id] = WakeWordProgress{Stage: stage, Fraction: frac}
			s.mu.Unlock()
			s.publish(EventProgress, map[string]any{"id": id, "stage": stage, "fraction": frac})
		},
	})
	if err != nil {
		var te *kws.TrainError
		switch {
		case ctx.Err() != nil:
			// deleted while it trained: nothing to mark
			s.d.Log.Info("wakeword_training_cancelled", "id", id)
		case errors.As(err, &te):
			fail(te.Code)
		default:
			fail("internal")
		}
		return
	}
	file, err := m.Marshal()
	if err != nil {
		fail("internal")
		return
	}
	uid, _ := parseID(id)
	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := s.d.Q.FinishWakeWordTraining(dctx, db.FinishWakeWordTrainingParams{
		ID: uid, Model: file, Sha256: text(sum(file)), Threshold: pgtype.Float4{Float32: m.Threshold, Valid: true},
		Takes: takes(m.Takes), Recall: float8(m.Recall, true),
		FalsePerHour: float8(m.FalsePerHour, true), EvalHours: float8(m.EvalHours, true),
		TrainedAt: pgtype.Timestamptz{Time: s.d.Now(), Valid: true},
	})
	if err != nil || n == 0 {
		s.d.Log.Warn("wakeword_store_failed", "id", id)
		return
	}
	s.d.Log.Info("wakeword_trained", "id", id, "takes", m.Takes, "threshold", m.Threshold, "recall", m.Recall,
		"false_per_hour", m.FalsePerHour, "ms", s.d.Now().Sub(began).Milliseconds())
	s.changed(id, "ready", "")
}

func (s *Service) negatives() (*kws.Negatives, error) {
	if s.d.Negatives == "" {
		return nil, ErrNoPath
	}
	b, err := os.ReadFile(s.d.Negatives)
	if err != nil {
		return nil, ErrNoNegatives
	}
	return kws.ReadNegatives(b)
}

// Rename sets a word's label.
func (s *Service) Rename(ctx context.Context, id, word string) (WakeWordView, error) {
	uid, ok := parseID(id)
	if !ok {
		return WakeWordView{}, ErrNotFound
	}
	word, err := checkWord(word)
	if err != nil {
		return WakeWordView{}, err
	}
	n, err := s.d.Q.RenameWakeWord(ctx, db.RenameWakeWordParams{ID: uid, Word: word})
	if err != nil {
		return WakeWordView{}, err
	}
	if n == 0 {
		return WakeWordView{}, ErrNotFound
	}
	v, err := s.Get(ctx, id)
	if err == nil {
		s.changed(id, v.Status, v.Code)
	}
	return v, err
}

// Delete removes a word, cancelling its training. A device that runs it keeps its cached copy until its config moves.
func (s *Service) Delete(ctx context.Context, id string) error {
	uid, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	if cancel, ok := s.jobs[id]; ok {
		cancel()
	}
	s.mu.Unlock()
	n, err := s.d.Q.DeleteWakeWord(ctx, uid)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.d.Log.Info("wakeword_deleted", "id", id)
	s.publish(EventChanged, map[string]any{"id": id, "status": "deleted"})
	return nil
}

// DeleteDataset drops a word's kept dataset (not while it trains).
func (s *Service) DeleteDataset(ctx context.Context, id string) error {
	uid, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	v, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if v.Status == "training" {
		return ErrTraining
	}
	if _, err := s.d.Q.DeleteWakeWordDataset(ctx, uid); err != nil {
		return err
	}
	s.d.Log.Info("wakeword_dataset_deleted", "id", id)
	s.changed(id, v.Status, v.Code)
	return nil
}

// Model is a ready word's model file and its sha256.
func (s *Service) Model(ctx context.Context, id string) (file []byte, sha string, err error) {
	uid, ok := parseID(id)
	if !ok {
		return nil, "", ErrNotFound
	}
	row, err := s.d.Q.GetWakeWordModel(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return row.Model, row.Sha256.String, nil
}

func (s *Service) changed(id, status, code string) {
	s.publish(EventChanged, map[string]any{"id": id, "status": status, "code": code})
}

func (s *Service) publish(kind string, data map[string]any) {
	if s.d.Bus == nil {
		return
	}
	s.d.Bus.Publish(events.Event{Type: kind, At: s.d.Now(), Data: data})
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// takes is a model's take count, which pkg/kws keeps under kws.MaxTakes (a model file's own is clamped there too)
func takes(n int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(min(max(n, 0), kws.MaxTakes)), Valid: n > 0} //nolint:gosec // clamped to 100
}

func text(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }

func float8(v float64, valid bool) pgtype.Float8 { return pgtype.Float8{Float64: v, Valid: valid} }

func parseID(id string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(id); err != nil || !u.Valid {
		return u, false
	}
	return u, true
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return hex.EncodeToString(b[:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:])
}
