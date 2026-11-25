package kws

// gemmGeneric is the portable kernel: acc[i][j] = sum over k of a[k][i] * w[k][j], a packed blockP and w blockC wide.
// It runs the block as two halves of 4x4, sixteen accumulators each, so a compiler keeps them in registers.
func gemmGeneric(a, w []float32, k int, acc *[blockP * blockC]float32) {
	a = a[:k*blockP]
	w = w[:k*blockC]
	for half := 0; half < blockC; half += 4 {
		var c00, c01, c02, c03, c10, c11, c12, c13, c20, c21, c22, c23, c30, c31, c32, c33 float32
		for kk := range k {
			x := a[kk*blockP : kk*blockP+4 : kk*blockP+4]
			v := w[kk*blockC+half : kk*blockC+half+4 : kk*blockC+half+4]
			x0, x1, x2, x3 := x[0], x[1], x[2], x[3]
			w0, w1, w2, w3 := v[0], v[1], v[2], v[3]
			c00 += x0 * w0
			c01 += x0 * w1
			c02 += x0 * w2
			c03 += x0 * w3
			c10 += x1 * w0
			c11 += x1 * w1
			c12 += x1 * w2
			c13 += x1 * w3
			c20 += x2 * w0
			c21 += x2 * w1
			c22 += x2 * w2
			c23 += x2 * w3
			c30 += x3 * w0
			c31 += x3 * w1
			c32 += x3 * w2
			c33 += x3 * w3
		}
		acc[0*blockC+half], acc[0*blockC+half+1], acc[0*blockC+half+2], acc[0*blockC+half+3] = c00, c01, c02, c03
		acc[1*blockC+half], acc[1*blockC+half+1], acc[1*blockC+half+2], acc[1*blockC+half+3] = c10, c11, c12, c13
		acc[2*blockC+half], acc[2*blockC+half+1], acc[2*blockC+half+2], acc[2*blockC+half+3] = c20, c21, c22, c23
		acc[3*blockC+half], acc[3*blockC+half+1], acc[3*blockC+half+2], acc[3*blockC+half+3] = c30, c31, c32, c33
	}
}
