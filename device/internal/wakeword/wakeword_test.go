package wakeword

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	"git.shino.boo/devilreef/cadence/pkg/kws"
)

const id = "0b5c3b1e-7d0c-4c5e-9a43-3d2f0f6a8b11"

// a model whose head always fires: zero weights and a large output bias
func alwaysModel(t *testing.T) []byte {
	t.Helper()
	h := &kws.Head{
		W1: make([]float32, kws.EmbeddingDim*kws.HeadProj), B1: make([]float32, kws.HeadProj),
		W2: make([]float32, kws.Context*kws.HeadProj*kws.HeadHidden), B2: make([]float32, kws.HeadHidden),
		W3: make([]float32, kws.HeadHidden), B3: []float32{20},
	}
	b, err := (&kws.Model{Word: "test", Threshold: 0.8, Head: h}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type server struct {
	mu    sync.Mutex
	body  []byte
	paths []string
}

func (s *server) Get(_ context.Context, path string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, path)
	if s.body == nil {
		return nil, errors.New("down")
	}
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func cfg(sha string) json.RawMessage {
	return json.RawMessage(`{"enabled":true,"modelId":"` + id + `","sha256":"` + sha + `"}`)
}

func latest(t *testing.T, c *Capability) state {
	t.Helper()
	var st state
	select {
	case b := <-c.State():
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
	default:
	}
	return st
}

func TestListensAndWakesWithThePreRollInstant(t *testing.T) {
	model := alwaysModel(t)
	srv := &server{body: model}
	type wake struct {
		score float32
		atMs  int64
	}
	wakes := make(chan wake, 4)
	c := New(Options{DataDir: t.TempDir(), Get: srv, Wake: func(s float32, at int64) { wakes <- wake{s, at} }})
	var sent []*audiodv1.Envelope
	var mu sync.Mutex
	c.ListenReady(func(e *audiodv1.Envelope) error { mu.Lock(); sent = append(sent, e); mu.Unlock(); return nil })
	if err := c.Apply(context.Background(), 1, cfg(hash(model))); err != nil {
		t.Fatal(err)
	}
	if st := latest(t, c); st.State != "listening" || st.Word != "test" {
		t.Fatalf("state %+v", st)
	}
	mu.Lock()
	if n := len(sent); n != 2 || !sent[n-1].GetWakeListen().GetEnabled() {
		t.Fatalf("listen not sent: %v", sent)
	}
	mu.Unlock()
	if len(srv.paths) != 1 || srv.paths[0] != "/v2/wakeword/"+id {
		t.Fatalf("fetched %v", srv.paths)
	}
	pcm := make([]byte, 640)
	for seq := uint64(1); seq <= 200; seq++ {
		c.MicPCM(&audiodv1.MicPcm{Seq: seq, AtMs: int64(1000 + seq), Pcm: pcm, Gap: seq == 1})
		time.Sleep(200 * time.Microsecond)
	}
	select {
	case w := <-wakes:
		if w.score < 0.99 || w.atMs <= 1000 {
			t.Fatalf("wake %+v", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wake")
	}

	// off: audiod stops listening, frames go nowhere
	if err := c.Apply(context.Background(), 2, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if sent[len(sent)-1].GetWakeListen().GetEnabled() {
		t.Fatal("still listening")
	}
	mu.Unlock()
	c.MicPCM(&audiodv1.MicPcm{Seq: 1, Pcm: pcm})
}

func TestTheCacheServesWithoutTheServer(t *testing.T) {
	model := alwaysModel(t)
	dir := t.TempDir()
	if err := New(Options{DataDir: dir, Get: &server{body: model}}).Apply(context.Background(), 1, cfg(hash(model))); err != nil {
		t.Fatal(err)
	}
	c := New(Options{DataDir: dir, Get: &server{}})
	if err := c.Apply(context.Background(), 1, cfg(hash(model))); err != nil {
		t.Fatal(err)
	}
	if st := latest(t, c); st.State != "listening" {
		t.Fatalf("state %+v", st)
	}
	_ = c.Apply(context.Background(), 2, json.RawMessage(`{"enabled":false}`))
}

func TestRefusals(t *testing.T) {
	model := alwaysModel(t)
	for name, tc := range map[string]struct {
		srv  *server
		cfg  json.RawMessage
		code string
	}{
		"server down":     {&server{}, cfg(hash(model)), "model_unavailable"},
		"other bytes":     {&server{body: append([]byte(" "), model...)}, cfg(hash(model)), "model_mismatch"},
		"not a model":     {&server{body: []byte("{}")}, cfg(hash([]byte("{}"))), "model_invalid"},
		"upper-case hash": {&server{body: model}, cfg(strings.ToUpper(hash(model))), "invalid"},
		"unknown field":   {&server{body: model}, json.RawMessage(`{"enabled":false,"x":1}`), "invalid"},
		"sensitivity":     {&server{body: model}, json.RawMessage(`{"enabled":false,"sensitivity":2}`), "invalid"},
	} {
		err := New(Options{DataDir: t.TempDir(), Get: tc.srv}).Apply(context.Background(), 1, tc.cfg)
		var ae *capability.ApplyError
		if !errors.As(err, &ae) || ae.Code != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
}
