package bluetooth

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
)

// ShortID is a device's id in state, logs and streams: the first 6 bytes of sha256(adapter address + device
// address), as 12 lowercase hex. Stable per device per host, and never the address.
func ShortID(adapterAddr, deviceAddr string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(adapterAddr) + strings.ToUpper(deviceAddr)))
	return hex.EncodeToString(sum[:6])
}

const (
	kindPhone      = "phone"
	kindComputer   = "computer"
	kindSpeaker    = "speaker"
	kindHeadphones = "headphones"
	kindOther      = "other"
)

// kind is the device's from its icon, else its class of device
func kind(icon string, class uint32) string {
	switch icon {
	case "phone":
		return kindPhone
	case "computer":
		return kindComputer
	case "audio-headphones", "audio-headset":
		return kindHeadphones
	case "audio-card":
		return kindSpeaker
	}
	minor := class >> 2 & 0x3f
	switch class >> 8 & 0x1f {
	case 0x01:
		return kindComputer
	case 0x02:
		return kindPhone
	case 0x04:
		switch minor {
		case 0x01, 0x02, 0x06:
			return kindHeadphones
		case 0x05, 0x07, 0x08, 0x0a:
			return kindSpeaker
		}
	}
	return kindOther
}

// avrcp volume (0-127) and our level (0-100), both ways
func toPercent(v uint8) int { return int(math.Round(float64(min(v, 127)) * 100 / 127)) }

func toAVRCP(level int) uint8 {
	return uint8(math.Round(float64(max(0, min(level, 100))) * 127 / 100)) //nolint:gosec // 0-127
}

// pcmLevel is a pcm's level: the louder channel, mute bits aside
func pcmLevel(v uint16) uint8 {
	return uint8(max(v>>8&0x7f, v&0x7f)) //nolint:gosec // 0-127
}
