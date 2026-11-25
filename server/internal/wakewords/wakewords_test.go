package wakewords

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/pkg/kws"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type bus struct {
	mu  sync.Mutex
	got []events.Event
}

func (b *bus) Publish(e events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, e)
}

func (b *bus) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.got {
		out = append(out, e.Type)
	}
	return out
}

type rig struct {
	s    *Service
	bus  *bus
	logs *bytes.Buffer
	dir  string
}

func newRig(t *testing.T, negatives bool) *rig {
	t.Helper()
	r := &rig{bus: &bus{}, logs: &bytes.Buffer{}, dir: t.TempDir()}
	path := filepath.Join(r.dir, "negatives.bin")
	if negatives {
		if err := writeNegatives(path, fakeNegatives(t)); err != nil {
			t.Fatal(err)
		}
	}
	r.s = New(Deps{Q: db.New(storetest.Pool(t)), Bus: r.bus, Negatives: path, Workers: 2, Epochs: 4,
		Log: slog.New(slog.NewTextHandler(r.logs, nil))})
	return r
}

func noise(r *rand.Rand, seconds, level float64) []int16 {
	pcm := make([]int16, int(seconds*kws.SampleRate))
	for i := range pcm {
		pcm[i] = int16(r.NormFloat64() * level)
	}
	return pcm
}

func take(r *rand.Rand) []int16 {
	pcm := noise(r, 1.5, 20)
	for i := range kws.SampleRate / 2 {
		t := float64(i) / kws.SampleRate
		pcm[kws.SampleRate/2+i] += int16(6000 * math.Sin(2*math.Pi*(300*t+900*t*t)))
	}
	return pcm
}

func fakeNegatives(t *testing.T) *kws.Negatives {
	t.Helper()
	f, err := kws.NewFeatures()
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(5, 5))
	var s []float32
	f.Push(noise(r, 8, 400), func(e []float32) { s = append(s, e...) })
	n := &kws.Negatives{Source: "test", Streams: [][]float32{s}}
	for e := kws.Context; e*kws.EmbeddingDim <= len(s); e += 4 {
		n.Windows = append(n.Windows, s[(e-kws.Context)*kws.EmbeddingDim:e*kws.EmbeddingDim])
	}
	return n
}

func dataset(t *testing.T) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(9, 9))
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	put := func(name string, pcm []int16) {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(kws.WAV16(pcm))
	}
	for i := range 4 {
		put(fmt.Sprintf("takes/%d.wav", i), take(r))
	}
	put("ambient.wav", noise(r, 6, 100))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func modelFile(t *testing.T) []byte {
	t.Helper()
	h := &kws.Head{
		W1: make([]float32, kws.EmbeddingDim*kws.HeadProj), B1: make([]float32, kws.HeadProj),
		W2: make([]float32, kws.Context*kws.HeadProj*kws.HeadHidden), B2: make([]float32, kws.HeadHidden),
		W3: make([]float32, kws.HeadHidden), B3: []float32{0},
	}
	b, err := (&kws.Model{Word: "Компьютер", Threshold: 0.8, Head: h}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitStatus(t *testing.T, s *Service, id, want string) WakeWordView {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		v, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == want {
			return v
		}
		if v.Status != "training" {
			t.Fatalf("status %s (%s), want %s", v.Status, v.Code, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("still training")
	return WakeWordView{}
}

func TestUploadRenameDelete(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	if _, err := r.s.UploadModel(ctx, "", []byte(`{"format":"x"}`)); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("got %v", err)
	}
	file := modelFile(t)
	v, err := r.s.UploadModel(ctx, "", file)
	if err != nil {
		t.Fatal(err)
	}
	if v.Word != "Компьютер" || v.Status != "ready" || v.Source != "upload" || v.SHA256 != sum(file) || v.Threshold == nil {
		t.Fatalf("view %+v", v)
	}
	got, sha, err := r.s.Model(ctx, v.ID)
	if err != nil || !bytes.Equal(got, file) || sha != v.SHA256 {
		t.Fatalf("model %v", err)
	}
	if v, err = r.s.Rename(ctx, v.ID, "Джарвис"); err != nil || v.Word != "Джарвис" {
		t.Fatalf("rename %v %+v", err, v)
	}
	if _, err := r.s.Rename(ctx, v.ID, strings.Repeat("x", 65)); !errors.Is(err, ErrInvalidWord) {
		t.Fatalf("got %v", err)
	}
	list, err := r.s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %d", err, len(list))
	}
	if err := r.s.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Model(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	// the word reaches no log
	if strings.Contains(r.logs.String(), "Джарвис") || strings.Contains(r.logs.String(), "Компьютер") {
		t.Fatal("a word in the log")
	}
}

func TestTrainKeepAndRetrain(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	if _, err := r.s.Train(ctx, "тест", []byte("nope"), false); err == nil {
		t.Fatal("a bad zip passed")
	} else if de := (*DatasetError)(nil); !errors.As(err, &de) || de.Code != "not_zip" {
		t.Fatalf("got %v", err)
	}
	v, err := r.s.Train(ctx, "тест", dataset(t), true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "training" || !v.DatasetKept {
		t.Fatalf("view %+v", v)
	}
	v = waitStatus(t, r.s, v.ID, "ready")
	if v.Recall == nil || v.Takes == nil || *v.Takes != 4 || v.ModelBytes == 0 {
		t.Fatalf("view %+v", v)
	}
	file, _, err := r.s.Model(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kws.ParseModel(file); err != nil {
		t.Fatal(err)
	}
	types := strings.Join(r.bus.types(), ",")
	if !strings.Contains(types, EventProgress) || !strings.HasSuffix(types, EventChanged) {
		t.Fatalf("events %s", types)
	}
	if _, err := r.s.Retrain(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, r.s, v.ID, "ready")
	if err := r.s.DeleteDataset(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Retrain(ctx, v.ID); !errors.Is(err, ErrNoDataset) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(r.logs.String(), "тест") {
		t.Fatal("the word in the log")
	}

	// not kept: dropped with the training
	v, err = r.s.Train(ctx, "другое", dataset(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if v = waitStatus(t, r.s, v.ID, "ready"); v.DatasetKept {
		t.Fatal("a dataset kept unasked")
	}
}

func TestTrainingWithoutNegativesFails(t *testing.T) {
	r := newRig(t, false)
	v, err := r.s.Train(context.Background(), "тест", dataset(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if v = waitStatus(t, r.s, v.ID, "failed"); v.Code != "no_negatives" {
		t.Fatalf("code %s", v.Code)
	}
	r.s.d.Negatives = ""
	if _, err := r.s.Train(context.Background(), "тест", dataset(t), false); !errors.Is(err, ErrNoPath) {
		t.Fatalf("got %v", err)
	}
}

func TestRunMarksInterruptedTrainings(t *testing.T) {
	r := newRig(t, false)
	row, err := r.s.d.Q.InsertWakeWord(context.Background(), db.InsertWakeWordParams{Word: "x", Source: "trained",
		Status: "training"})
	if err != nil {
		t.Fatal(err)
	}
	r.s.Run(context.Background())()
	v, err := r.s.Get(context.Background(), uuidString(row.ID))
	if err != nil || v.Status != "failed" || v.Code != "interrupted" {
		t.Fatalf("%v %+v", err, v)
	}
}

// npy files with the features' layouts, served by range
type npyFetcher map[string][]byte

func npyFile(descr string, shape []int, values int, size int) []byte {
	dims := ""
	for _, d := range shape {
		dims += fmt.Sprintf("%d, ", d)
	}
	header := fmt.Sprintf("{'descr': '%s', 'fortran_order': False, 'shape': (%s), }", descr, strings.TrimSuffix(dims, " "))
	for (10+len(header)+1)%64 != 0 {
		header += " "
	}
	header += "\n"
	b := append([]byte("\x93NUMPY\x01\x00"), byte(len(header)), byte(len(header)>>8))
	b = append(b, header...)
	return append(b, make([]byte, values*size)...)
}

func (f npyFetcher) Range(_ context.Context, file string, offset, length int64) ([]byte, error) {
	b, ok := f[file]
	if !ok || offset+length > int64(len(b)) {
		if ok && offset == 0 {
			return b, nil
		}
		return nil, errors.New("range")
	}
	return b[offset : offset+length], nil
}

func TestFetchNegatives(t *testing.T) {
	r := newRig(t, false)
	acav := npyFile("<f2", []int{3000, 16, 96}, 3000*16*96, 2)
	// a recognisable value in the first window
	binary.LittleEndian.PutUint16(acav[len(acav)-3000*16*96*2:], kws.ToHalf(1.5))
	r.s.d.Fetch = npyFetcher{acavFile: acav, validationFile: npyFile("<f4", []int{40000, 96}, 40000*96, 4)}
	if err := r.s.FetchNegatives(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := r.s.Negatives()
		if v.Fetching != nil && v.Fetching.Done {
			if v.Fetching.Code != "" || !v.Present || v.Windows != windowChunks*windowsInChunk || v.Streams != streamCount ||
				!strings.Contains(v.Source, negativesLicense) {
				t.Fatalf("negatives %+v", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fetch never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeviceRoute(t *testing.T) {
	r := newRig(t, false)
	file := modelFile(t)
	v, err := r.s.UploadModel(context.Background(), "x", file)
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	r.s.MountDevice(app.Group("/v2"), func(_ context.Context, token string) (string, error) {
		if token != "good" {
			return "", auth.ErrUnauthorized
		}
		return "dev-1", nil
	})
	get := func(path, token string) (int, []byte) {
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, _ := get("/v2/wakeword/"+v.ID, ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := get("/v2/wakeword/"+v.ID, "bad"); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := get("/v2/wakeword/0b5c3b1e-7d0c-4c5e-9a43-3d2f0f6a8b11", "good"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	code, body := get("/v2/wakeword/"+v.ID, "good")
	if code != 200 || !bytes.Equal(body, file) {
		t.Fatalf("got %d", code)
	}
}
