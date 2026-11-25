package kws

import (
	"fmt"
)

// The embedding net, Google's speech_embedding as openWakeWord ships it: 76 mel frames x 32 bins in, 96 values out.
// Twenty convolutions, each over (time, mel) with its taps' time axis unpadded and its mel axis padded to keep its
// width; after every one but the last a LeakyReLU(0.2) floored at -0.4. Max pools follow convolutions 2, 10 and 18
// (2x2: time and mel halved) and 6 and 14 (1x2: mel alone). The time axis shrinks by 8 overall, so the net run over a
// stream yields one embedding per 8 frames, each the window's: run here as a stream, every layer keeps only the rows
// its next outputs still need, and a step of 8 new frames costs about an eighth of a whole window.
const (
	// EmbeddingDim is the size of one embedding
	EmbeddingDim = 96
	// WindowFrames is the mel frames one embedding covers (775 ms)
	WindowFrames = 76

	leakSlope = 0.2
	leakFloor = -0.4
	// a block of the packed matrices: 4 output positions by 8 output channels
	blockP = 4
	blockC = 8
	// zero frames a stream starts with, so the windows the net's strides give end on the steps' boundaries
	streamPad = stepFrames - WindowFrames%stepFrames
)

// layer is one convolution or pool of the stream. Rows are time steps; a row is width x channels, channels last.
type layer struct {
	conv          bool
	kh, kw        int // a conv's taps; a pool's time and mel strides
	width, cin    int // the input row's shape
	outW, cout    int
	k             int       // a conv's inner size: kh x kw x cin
	weights       []float32 // packed: [cout/blockC][k][blockC]
	bias          []float32
	act           bool
	rows          []float32 // the input rows kept, then the new ones
	have          int
	cols, outRows []float32 // scratch: the packed inputs, the output rows
}

type embedNet struct {
	layers []*layer
	// outputs seen since the reset: the first covers the pad
	outputs int
}

func newEmbedNet(t map[string]tensor) (*embedNet, error) {
	net := &embedNet{}
	width, ch := MelBins, 1
	for i := 0; ; i++ {
		raw, ok := t[fmt.Sprintf("conv%d.w", i)]
		if !ok {
			break
		}
		if len(raw.shape) != 4 || raw.shape[1] != ch {
			return nil, fmt.Errorf("kws: conv%d is %v on %d channels", i, raw.shape, ch)
		}
		cout, kh, kw := raw.shape[0], raw.shape[2], raw.shape[3]
		bias, err := lookup(t, fmt.Sprintf("conv%d.b", i), cout)
		if err != nil {
			return nil, err
		}
		if cout%blockC != 0 || kw%2 == 0 {
			return nil, fmt.Errorf("kws: conv%d has %d outputs and %d taps", i, cout, kw)
		}
		l := &layer{conv: true, kh: kh, kw: kw, width: width, cin: ch, outW: width, cout: cout, k: kh * kw * ch,
			bias: bias, act: true}
		l.weights = make([]float32, cout*l.k)
		for o := range cout {
			for c := range ch {
				for y := range kh {
					for x := range kw {
						kk := (y*kw+x)*ch + c
						l.weights[((o/blockC)*l.k+kk)*blockC+o%blockC] = raw.data[((o*ch+c)*kh+y)*kw+x]
					}
				}
			}
		}
		net.layers = append(net.layers, l)
		ch = cout
		switch i {
		case 2, 10, 18:
			net.layers = append(net.layers, &layer{kh: 2, kw: 2, width: width, cin: ch, outW: width / 2, cout: ch})
			width /= 2
		case 6, 14:
			net.layers = append(net.layers, &layer{kh: 1, kw: 2, width: width, cin: ch, outW: width / 2, cout: ch})
			width /= 2
		}
	}
	if n := len(net.layers); n != 25 || width != 1 || ch != EmbeddingDim {
		return nil, fmt.Errorf("kws: embedding net of %d layers ends %dx%d", n, width, ch)
	}
	net.layers[len(net.layers)-1].act = false
	// a layer gets at most a step's rows on top of what it keeps
	for _, l := range net.layers {
		keep := l.kh - 1
		l.rows = make([]float32, (keep+stepFrames)*l.width*l.cin)
		l.cols = make([]float32, (stepFrames*l.outW+blockP)*max(l.k, 1))
		l.outRows = make([]float32, (stepFrames*l.outW+blockP)*l.cout)
	}
	net.reset()
	return net, nil
}

func (n *embedNet) reset() {
	for _, l := range n.layers {
		l.have = 0
	}
	n.outputs = 0
	var pad [streamPad * MelBins]float32
	n.push(pad[:], streamPad, nil)
}

// push runs rows new mel frames through the net and hands each embedding they complete to emit (the slice is reused).
func (n *embedNet) push(frames []float32, rows int, emit func([]float32)) {
	in := frames
	for _, l := range n.layers {
		in, rows = l.push(in, rows)
		if rows == 0 {
			return
		}
	}
	for r := range rows {
		n.outputs++
		// the first output's window reaches into the pad
		if n.outputs > 1 && emit != nil {
			emit(in[r*EmbeddingDim : (r+1)*EmbeddingDim])
		}
	}
}

// push appends rows input rows and answers the output rows they complete.
func (l *layer) push(in []float32, rows int) ([]float32, int) {
	row := l.width * l.cin
	copy(l.rows[l.have*row:], in[:rows*row])
	l.have += rows
	var out int
	if l.conv {
		out = max(l.have-l.kh+1, 0)
		if out > 0 {
			l.convolve(out)
		}
	} else {
		out = l.have / l.kh
		l.pool(out)
	}
	used := out * l.kh
	if l.conv {
		used = out
	}
	copy(l.rows, l.rows[used*row:l.have*row])
	l.have -= used
	return l.outRows, out
}

func (l *layer) convolve(out int) {
	positions := out * l.outW
	pad := l.kw / 2
	cols := l.cols
	// packed by blocks of blockP positions: [block][k][blockP]; positions past the last are zero
	blocks := (positions + blockP - 1) / blockP
	clear(cols[:blocks*l.k*blockP])
	for p := range positions {
		y, x := p/l.outW, p%l.outW
		dst := cols[(p/blockP)*l.k*blockP+p%blockP:]
		kk := 0
		for ky := range l.kh {
			src := l.rows[(y+ky)*l.width*l.cin:]
			for kx := range l.kw {
				xx := x + kx - pad
				if xx < 0 || xx >= l.width {
					kk += l.cin
					continue
				}
				s := src[xx*l.cin : (xx+1)*l.cin]
				d := dst[kk*blockP : (kk+l.cin-1)*blockP+1]
				for c, v := range s {
					d[c*blockP] = v
				}
				kk += l.cin
			}
		}
	}
	var acc [blockP * blockC]float32
	for b := range blocks {
		a := cols[b*l.k*blockP : (b+1)*l.k*blockP]
		for ob := range l.cout / blockC {
			gemmBlock(a, l.weights[ob*l.k*blockC:(ob+1)*l.k*blockC], l.k, &acc)
			for i := range blockP {
				p := b*blockP + i
				if p >= positions {
					break
				}
				o := l.outRows[p*l.cout+ob*blockC : p*l.cout+(ob+1)*blockC]
				for j := range o {
					v := acc[i*blockC+j] + l.bias[ob*blockC+j]
					if l.act && v < 0 {
						v = max(v*leakSlope, leakFloor)
					}
					o[j] = v
				}
			}
		}
	}
}

// pool is a max pool of kh rows by kw columns, each its own stride.
func (l *layer) pool(out int) {
	c := l.cin
	for y := range out {
		for x := range l.outW {
			o := l.outRows[(y*l.outW+x)*c : (y*l.outW+x+1)*c]
			for ty := range l.kh {
				for tx := range l.kw {
					src := l.rows[((y*l.kh+ty)*l.width+x*l.kw+tx)*c:]
					src = src[:c]
					if ty == 0 && tx == 0 {
						copy(o, src)
						continue
					}
					for i, v := range src {
						o[i] = max(o[i], v)
					}
				}
			}
		}
	}
}
