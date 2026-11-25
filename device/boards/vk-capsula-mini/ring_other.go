//go:build !linux

package vkcapsula

type topleds struct{}

func openTopleds(string) *topleds { return nil }

func (*topleds) set(*[ringChannelCount]byte) error { return nil }

func (*topleds) close() {}
