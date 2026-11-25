// Package receivername is the name a media receiver (AirPlay, Spotify Connect) advertises on the network: the one its
// capability config sets, else the device's own, else Default.
package receivername

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Default is the name a receiver advertises while neither its config nor the device names one.
const Default = "Cadence"

// Pick is configured when it is set, else the device's name as From has it, else Default.
func Pick(configured, device string, maxBytes int) string {
	if configured != "" {
		return configured
	}
	if n := From(device, maxBytes); n != "" {
		return n
	}
	return Default
}

// From is the device's name as a receiver can advertise it: no control, format or line-breaking character, white
// space trimmed, at most maxBytes bytes cut at a character's boundary (a byte limit, as the mDNS label it goes into
// has one); "" when nothing is left.
func From(device string, maxBytes int) string {
	device = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, device)
	device = strings.TrimSpace(device)
	if len(device) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(device[cut]) {
			cut--
		}
		device = strings.TrimSpace(device[:cut])
	}
	return device
}
