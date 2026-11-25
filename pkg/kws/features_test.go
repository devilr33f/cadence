package kws

import (
	"math"
	"os"
	"testing"
)

// the golden vectors are onnxruntime's on openWakeWord's own models (tools/kws/export.py, make kws-assets)
func golden(t testing.TB) map[string]tensor {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.bin")
	if err != nil {
		t.Fatal(err)
	}
	g, err := readTensors(b)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func goldenPCM(g map[string]tensor) []int16 {
	audio := g["audio"].data
	pcm := make([]int16, len(audio))
	for i, v := range audio {
		pcm[i] = int16(v)
	}
	return pcm
}

func TestMelMatchesOnnx(t *testing.T) {
	g := golden(t)
	f, err := NewFeatures()
	if err != nil {
		t.Fatal(err)
	}
	audio := g["audio"].data
	want := g["mel"].data
	padded := append(make([]float32, ctxSamples), audio...)
	out := make([]float32, stepFrames*MelBins)
	worst := 0.0
	for s := 0; s*StepSamples+callSamples <= len(padded); s++ {
		f.mel.step(padded[s*StepSamples:s*StepSamples+callSamples], out)
		for i, v := range out {
			worst = math.Max(worst, math.Abs(float64(v-want[s*stepFrames*MelBins+i])))
		}
	}
	// the log scale over /10: 1e-3 is 0.01 dB
	if worst > 1e-3 {
		t.Fatalf("mel differs from onnx by %g", worst)
	}
}

func TestEmbeddingsMatchOnnx(t *testing.T) {
	g := golden(t)
	f, err := NewFeatures()
	if err != nil {
		t.Fatal(err)
	}
	want := g["emb"]
	var got [][]float32
	// odd-sized pushes: a step completes mid-push
	pcm := goldenPCM(g)
	for len(pcm) > 0 {
		n := min(len(pcm), 333)
		f.Push(pcm[:n], func(e []float32) { got = append(got, append([]float32(nil), e...)) })
		pcm = pcm[n:]
	}
	if len(got) != want.shape[0] {
		t.Fatalf("got %d embeddings, want %d", len(got), want.shape[0])
	}
	worst := 0.0
	for i, e := range got {
		for j, v := range e {
			worst = math.Max(worst, math.Abs(float64(v-want.data[i*EmbeddingDim+j])))
		}
	}
	if worst > 1e-3 {
		t.Fatalf("embeddings differ from onnx by %g", worst)
	}
	t.Logf("worst difference %g over %d embeddings", worst, len(got))

	// a reset stream gives the same embeddings again
	f.Reset()
	var again int
	f.Push(goldenPCM(g), func(e []float32) {
		for j, v := range e {
			if math.Abs(float64(v-got[again][j])) > 1e-6 {
				t.Fatalf("embedding %d differs after a reset", again)
			}
		}
		again++
	})
	if again != len(got) {
		t.Fatalf("%d embeddings after a reset, want %d", again, len(got))
	}
}

// BenchmarkStep is one 80 ms step of the device's work: the mel frames and the embedding they complete.
func BenchmarkStep(b *testing.B) {
	g := golden(b)
	f, err := NewFeatures()
	if err != nil {
		b.Fatal(err)
	}
	pcm := goldenPCM(g)
	// warm the stream up past its first window
	f.Push(pcm[:StepSamples*12], func([]float32) {})
	step := pcm[StepSamples*12 : StepSamples*13]
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		f.Push(step, func([]float32) {})
	}
}
