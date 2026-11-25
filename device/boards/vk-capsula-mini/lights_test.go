package vkcapsula_test

import (
	"testing"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func sink(t *testing.T, tr *tree) leds.Sink {
	t.Helper()
	b := vkcapsula.New(tr.path("data/cadence"), nil, vkcapsula.WithRoot(tr.root))
	s, ok := b.Lights()
	if !ok {
		t.Fatal("no lights")
	}
	return s
}

func TestGeometry(t *testing.T) {
	g := sink(t, capsulaTree(t)).Geometry()
	want := leds.Geometry{Ring: 12, Button: 0, Digits: 4, Segments: 7, LightFull: 2300}
	if g != want {
		t.Fatalf("geometry = %+v", g)
	}
}

func TestLightSensorReadsLux(t *testing.T) {
	tr := capsulaTree(t)
	tr.write("sys/bus/iio/devices/iio:device0/name", "other\n")
	tr.write("sys/bus/iio/devices/iio:device0/in_illuminance_both_input", "9\n")
	tr.write("sys/bus/iio/devices/iio:device1/name", "bh1730\n")
	tr.write("sys/bus/iio/devices/iio:device1/in_illuminance_both_input", "123.456\n")
	ls, ok := sink(t, tr).(leds.LightSensor)
	if !ok {
		t.Fatal("no light sensor")
	}
	if v, ok := ls.Light(); !ok || v != 123 {
		t.Fatalf("light = %d, %t", v, ok)
	}
	// rebound under another index: found again by name
	tr.remove("sys/bus/iio/devices/iio:device1")
	if _, ok := ls.Light(); ok {
		t.Fatal("a reading without the sensor")
	}
	tr.write("sys/bus/iio/devices/iio:device4/name", "bh1730\n")
	tr.write("sys/bus/iio/devices/iio:device4/in_illuminance_both_input", "7.9\n")
	if v, ok := ls.Light(); !ok || v != 7 {
		t.Fatalf("light after the sensor moved = %d, %t", v, ok)
	}
}
