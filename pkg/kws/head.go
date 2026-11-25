package kws

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
)

// The per-word head: the last Context embeddings (1.28 s) in, the probability the word just ended out. Each embedding
// is projected to HeadProj values by one matrix shared across time (so a word said a little earlier or later looks
// the same), the Context projections go through a dense layer of HeadHidden, then a sigmoid output; ReLU between.
// About 12k parameters, small enough to learn from a dozen takes without memorising them.
const (
	// Context is the embeddings a head reads
	Context    = 16
	HeadProj   = 24
	HeadHidden = 24

	flatProj = Context * HeadProj
)

// Head holds the weights, row-major: W1 [EmbeddingDim][HeadProj], W2 [flatProj][HeadHidden], W3 [HeadHidden].
type Head struct {
	W1 []float32 `json:"w1"`
	B1 []float32 `json:"b1"`
	W2 []float32 `json:"w2"`
	B2 []float32 `json:"b2"`
	W3 []float32 `json:"w3"`
	B3 []float32 `json:"b3"` // one value
}

func newHead(r *rand.Rand) *Head {
	h := zeroHead()
	initHe(r, h.W1, EmbeddingDim)
	initHe(r, h.W2, flatProj)
	initHe(r, h.W3, HeadHidden)
	return h
}

func zeroHead() *Head {
	return &Head{
		W1: make([]float32, EmbeddingDim*HeadProj), B1: make([]float32, HeadProj),
		W2: make([]float32, flatProj*HeadHidden), B2: make([]float32, HeadHidden),
		W3: make([]float32, HeadHidden), B3: make([]float32, 1),
	}
}

func initHe(r *rand.Rand, w []float32, fanIn int) {
	scale := math.Sqrt(2 / float64(fanIn))
	for i := range w {
		w[i] = float32(r.NormFloat64() * scale)
	}
}

// Check refuses a head of other sizes or with values that are not finite: a corrupt model must not crash the device.
func (h *Head) Check() error {
	if h == nil {
		return errors.New("kws: no head")
	}
	sizes := []struct {
		v    []float32
		want int
	}{{h.W1, EmbeddingDim * HeadProj}, {h.B1, HeadProj}, {h.W2, flatProj * HeadHidden}, {h.B2, HeadHidden},
		{h.W3, HeadHidden}, {h.B3, 1}}
	for i, s := range sizes {
		if len(s.v) != s.want {
			return fmt.Errorf("kws: head tensor %d has %d values, want %d", i, len(s.v), s.want)
		}
		for _, v := range s.v {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return errors.New("kws: head has a value that is not finite")
			}
		}
	}
	return nil
}

// pass keeps one forward pass's activations for the backward one.
type pass struct {
	proj   [flatProj]float32 // after the ReLU
	hidden [HeadHidden]float32
}

// Score is the head's probability for a window of Context embeddings, oldest first (Context x EmbeddingDim).
func (h *Head) Score(window []float32) float32 {
	var p pass
	return h.forward(window, &p)
}

func (h *Head) forward(x []float32, p *pass) float32 {
	x = x[:Context*EmbeddingDim]
	for t := range Context {
		out := p.proj[t*HeadProj : (t+1)*HeadProj]
		copy(out, h.B1)
		for i, v := range x[t*EmbeddingDim : (t+1)*EmbeddingDim] {
			w := h.W1[i*HeadProj : (i+1)*HeadProj]
			for j := range out {
				out[j] += v * w[j]
			}
		}
		for j, v := range out {
			out[j] = max(v, 0)
		}
	}
	copy(p.hidden[:], h.B2)
	for i, v := range p.proj {
		if v == 0 {
			continue
		}
		w := h.W2[i*HeadHidden : (i+1)*HeadHidden]
		for j := range p.hidden {
			p.hidden[j] += v * w[j]
		}
	}
	z := h.B3[0]
	for j, v := range p.hidden {
		v = max(v, 0)
		p.hidden[j] = v
		z += v * h.W3[j]
	}
	return sigmoid(z)
}

func sigmoid(z float32) float32 { return float32(1 / (1 + math.Exp(-float64(z)))) }

// backward adds the gradient of weight x binary cross-entropy at (p, y) to g; a forward pass of x filled ps.
func (h *Head) backward(x []float32, ps *pass, p, y, weight float32, g *Head) {
	dz := weight * (p - y)
	g.B3[0] += dz
	var dHidden [HeadHidden]float32
	for j, v := range ps.hidden {
		g.W3[j] += dz * v
		if v > 0 {
			dHidden[j] = dz * h.W3[j]
			g.B2[j] += dHidden[j]
		}
	}
	var dProj [flatProj]float32
	for i, v := range ps.proj {
		if v == 0 {
			continue
		}
		w := h.W2[i*HeadHidden : (i+1)*HeadHidden]
		gw := g.W2[i*HeadHidden : (i+1)*HeadHidden]
		var s float32
		for j, d := range dHidden {
			gw[j] += v * d
			s += w[j] * d
		}
		dProj[i] = s
	}
	for t := range Context {
		d := dProj[t*HeadProj : (t+1)*HeadProj]
		for j, v := range d {
			g.B1[j] += v
		}
		for i, v := range x[t*EmbeddingDim : (t+1)*EmbeddingDim] {
			if v == 0 {
				continue
			}
			gw := g.W1[i*HeadProj : (i+1)*HeadProj]
			for j, dv := range d {
				gw[j] += v * dv
			}
		}
	}
}

// tensors lists the weights in a fixed order.
func (h *Head) tensors() [][]float32 { return [][]float32{h.W1, h.B1, h.W2, h.B2, h.W3, h.B3} }
