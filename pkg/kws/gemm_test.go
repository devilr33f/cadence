package kws

import (
	"math"
	"math/rand/v2"
	"testing"
)

// the platform's kernel (neon on arm64) agrees with the portable one
func TestGemmBlockMatchesGeneric(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, k := range []int{1, 3, 72, 288} {
		a := make([]float32, k*blockP)
		w := make([]float32, k*blockC)
		for i := range a {
			a[i] = float32(r.NormFloat64())
		}
		for i := range w {
			w[i] = float32(r.NormFloat64())
		}
		var got, want [blockP * blockC]float32
		gemmBlock(a, w, k, &got)
		gemmGeneric(a, w, k, &want)
		for i := range got {
			if math.Abs(float64(got[i]-want[i])) > 1e-4*math.Sqrt(float64(k)) {
				t.Fatalf("k %d: acc[%d] = %g, want %g", k, i, got[i], want[i])
			}
		}
	}
}
