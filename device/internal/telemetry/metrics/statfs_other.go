//go:build !linux

package metrics

import "errors"

// Statfs reads nothing off linux: every board runs it.
func Statfs(string) (total, free uint64, err error) {
	return 0, 0, errors.New("metrics: statfs needs linux")
}
