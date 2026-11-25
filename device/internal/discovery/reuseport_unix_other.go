//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package discovery

// no SO_REUSEPORT known here: SO_REUSEADDR alone
const soReusePort = 0
