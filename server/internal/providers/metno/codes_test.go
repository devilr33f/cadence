package metno

import "testing"

func TestSymbolCodesUseWMOVocabulary(t *testing.T) {
	cases := map[string]int{
		"clearsky_day": 0, "clearsky_night": 0, "fair_polartwilight": 1, "partlycloudy": 2, "cloudy": 3, "fog": 45,
		"lightrain": 61, "rain": 63, "heavyrain": 65,
		"lightrainshowers": 80, "rainshowers": 81, "heavyrainshowers": 82,
		"lightsnow": 71, "snow": 73, "heavysnow": 75,
		"lightsnowshowers": 85, "snowshowers": 85, "heavysnowshowers": 86,
		"lightsleet": 66, "sleet": 66, "heavysleet": 67,
		"lightsleetshowers": 80, "sleetshowers": 81, "heavysleetshowers": 82,
		"rainandthunder": 95, "heavyrainandthunder": 97, "snowshowersandthunder": 95,
	}
	for symbol, want := range cases {
		got, ok := symbolCode(symbol)
		if !ok || got != want {
			t.Errorf("%s: code %d, %v; want %d", symbol, got, ok, want)
		}
	}
	if _, ok := symbolCode("unannounced_future_symbol"); ok {
		t.Fatal("unknown symbol became a known condition")
	}
}

func TestTruncateCoordinate(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{55.755869, 55.7558}, {37.617299, 37.6172}, {-37.617299, -37.6172}, {0, 0},
	} {
		if got := truncateCoordinate(tc.in); got != tc.want {
			t.Errorf("%f: %f, want %f", tc.in, got, tc.want)
		}
	}
}
