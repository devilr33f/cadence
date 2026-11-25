package leds

// VolumeOverlay is the volume level drawn over whatever the state shows.
type VolumeOverlay struct {
	// the number on the clock
	Level int
	// 0..1 of the ring lit, gliding towards Level
	Fill float64
	// 0..1, fading in and out over the state underneath
	Opacity float64
}

const volumeLit = 0.75

func (o VolumeOverlay) draw(g Geometry, paint painter) Frame {
	f := newFrame(g)
	n := float64(len(f.Ring))
	lit := clamp01(o.Fill) * n
	for i := range f.Ring {
		pos := float64(i)
		// the led at the edge of the fill is partly lit, so the fill glides between steps
		f.Ring[i] = paint.colour(volumeColour((pos+0.5)/n), volumeLit*clamp01(lit-pos))
	}
	numberGlyphs(o.Level).show(&f, paint.segment(1))
	return f
}

// foldOverlays blends each overlay over the frame in list order, each by its own opacity, so a later overlay covers
// the ones before it. brightness is the room's, as for the state underneath
func foldOverlays(base Frame, overlays []VolumeOverlay, brightness float64) Frame {
	g := geometryOf(base)
	paint := newPainter(brightness)
	for _, o := range overlays {
		base = Blend(base, o.draw(g, paint), o.Opacity)
	}
	return base
}
