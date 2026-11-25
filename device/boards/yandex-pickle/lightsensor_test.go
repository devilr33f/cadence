package pickle_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func light(t *testing.T, s leds.Sink) (int, bool) {
	t.Helper()
	ls, ok := s.(leds.LightSensor)
	if !ok {
		t.Fatal("the sink has no light sensor")
	}
	return ls.Light()
}

func TestLightSensorFoundByName(t *testing.T) {
	tr := pickleTree(t)
	tr.write("sys/bus/iio/devices/"+iioDir(0)+"/in_intensity_raw", "99\n")
	tr.write("sys/bus/iio/devices/"+iioDir(1)+"/in_intensity_raw", "4\n")
	s := sink(t, tr)
	if v, ok := light(t, s); !ok || v != 4 {
		t.Fatalf("light = %d, %t", v, ok)
	}
	tr.write("sys/bus/iio/devices/"+iioDir(1)+"/in_intensity_raw", "0\n")
	if v, ok := light(t, s); !ok || v != 0 {
		t.Fatalf("light in the dark = %d, %t", v, ok)
	}
}

func TestLightSensorUnreadable(t *testing.T) {
	tr := pickleTree(t)
	s := sink(t, tr)
	if _, ok := light(t, s); ok {
		t.Fatal("a reading without the value file")
	}
	for _, text := range []string{"", "four\n", " 4\n", "4 lux\n"} {
		tr.write("sys/bus/iio/devices/"+iioDir(1)+"/in_intensity_raw", text)
		if v, ok := light(t, s); ok {
			t.Fatalf("%q read as %d", text, v)
		}
	}
	// the driver was rebound under another index: it is found by its name on the next read
	tr.remove("sys/bus/iio/devices/" + iioDir(1))
	if _, ok := light(t, s); ok {
		t.Fatal("a reading without the sensor")
	}
	tr.write("sys/bus/iio/devices/"+iioDir(5)+"/name", "ltr308als01\n")
	tr.write("sys/bus/iio/devices/"+iioDir(5)+"/in_intensity_raw", "3\n")
	if v, ok := light(t, s); !ok || v != 3 {
		t.Fatalf("light after the sensor moved = %d, %t", v, ok)
	}
}
