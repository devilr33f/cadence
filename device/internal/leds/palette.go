package leds

// "aurora": state colours as srgb; the painter gamma corrects them for the linear leds
var (
	listeningColour = RGB{0, 230, 170}
	thinkingColour  = RGB{180, 70, 255}
	speakingColour  = RGB{60, 130, 255}
	offlineColour   = RGB{255, 40, 60}
	ringingColour   = RGB{255, 200, 140}
)

// the volume ring runs from green at the quiet end through yellow to red at the loud end
var (
	volumeLow  = RGB{40, 230, 60}
	volumeMid  = RGB{255, 200, 0}
	volumeHigh = RGB{255, 40, 40}
)

const volumeMidAt = 0.6

// position 0..1 along the volume ring
func volumeColour(position float64) RGB {
	if position < volumeMidAt {
		return towardsRGB(volumeLow, volumeMid, clamp01(position/volumeMidAt))
	}
	return towardsRGB(volumeMid, volumeHigh, clamp01((position-volumeMidAt)/(1-volumeMidAt)))
}
