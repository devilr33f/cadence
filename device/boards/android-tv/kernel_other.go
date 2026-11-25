//go:build !linux && !android

package androidtv

// the tests' hosts have no kernel to ask: the board's answers then come from the app's word or the probe alone
func unameMachine() string            { return "" }
func personalityClearedUname() string { return "" }
func sysctlArch() string              { return "" }
