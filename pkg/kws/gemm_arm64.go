//go:build arm64 && !purego

package kws

//go:noescape
func gemmNEON(a, w *float32, k int, acc *[blockP * blockC]float32)

// gemmBlock runs the neon kernel; the slices' bounds are checked here, as the assembly checks none.
func gemmBlock(a, w []float32, k int, acc *[blockP * blockC]float32) {
	if k <= 0 || len(a) < k*blockP || len(w) < k*blockC {
		gemmGeneric(a, w, k, acc)
		return
	}
	gemmNEON(&a[0], &w[0], k, acc)
}
