//go:build !arm64 || purego

package kws

func gemmBlock(a, w []float32, k int, acc *[blockP * blockC]float32) { gemmGeneric(a, w, k, acc) }
