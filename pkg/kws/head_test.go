package kws

import (
	"math"
	"math/rand/v2"
	"testing"
)

// the analytic gradient agrees with a finite difference of the loss
func TestHeadGradient(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	h := newHead(r)
	x := make([]float32, Context*EmbeddingDim)
	for i := range x {
		x[i] = float32(r.NormFloat64())
	}
	const y = 1
	loss := func() float64 {
		p := float64(h.Score(x))
		return -(y*math.Log(p) + (1-y)*math.Log(1-p))
	}
	var ps pass
	p := h.forward(x, &ps)
	g := zeroHead()
	h.backward(x, &ps, p, y, 1, g)
	params, grads := h.tensors(), g.tensors()
	for ti, w := range params {
		for _, i := range []int{0, len(w) / 2, len(w) - 1} {
			const eps = 1e-2
			orig := w[i]
			w[i] = orig + eps
			up := loss()
			w[i] = orig - eps
			down := loss()
			w[i] = orig
			num := (up - down) / (2 * eps)
			if math.Abs(num-float64(grads[ti][i])) > 2e-2*math.Max(1, math.Abs(num)) {
				t.Fatalf("tensor %d [%d]: gradient %g, numeric %g", ti, i, grads[ti][i], num)
			}
		}
	}
}

func TestHeadCheckRefusesBadSizes(t *testing.T) {
	h := newHead(rand.New(rand.NewPCG(1, 1)))
	if err := h.Check(); err != nil {
		t.Fatal(err)
	}
	h.W2 = h.W2[:10]
	if h.Check() == nil {
		t.Fatal("a short tensor passed")
	}
	h = newHead(rand.New(rand.NewPCG(1, 1)))
	h.B1[0] = float32(math.NaN())
	if h.Check() == nil {
		t.Fatal("a NaN passed")
	}
}
