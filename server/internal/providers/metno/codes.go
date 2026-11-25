package metno

import "strings"

// symbolCode maps Locationforecast's symbols to the WMO codes Open-Meteo uses.
func symbolCode(symbol string) (int, bool) {
	for _, suffix := range []string{"_day", "_night", "_polartwilight"} {
		symbol = strings.TrimSuffix(symbol, suffix)
	}
	code, ok := map[string]int{
		"clearsky": 0, "fair": 1, "partlycloudy": 2, "cloudy": 3, "fog": 45,
		"lightrain": 61, "rain": 63, "heavyrain": 65,
		"lightsleet": 66, "sleet": 66, "heavysleet": 67,
		"lightsnow": 71, "snow": 73, "heavysnow": 75,
		"lightrainshowers": 80, "rainshowers": 81, "heavyrainshowers": 82,
		"lightsleetshowers": 80, "sleetshowers": 81, "heavysleetshowers": 82,
		"lightsnowshowers": 85, "snowshowers": 85, "heavysnowshowers": 86,
		"lightrainandthunder": 95, "rainandthunder": 95, "heavyrainandthunder": 97,
		"lightsleetandthunder": 95, "sleetandthunder": 95, "heavysleetandthunder": 97,
		"lightsnowandthunder": 95, "snowandthunder": 95, "heavysnowandthunder": 97,
		"lightrainshowersandthunder": 95, "rainshowersandthunder": 95, "heavyrainshowersandthunder": 97,
		"lightsleetshowersandthunder": 95, "sleetshowersandthunder": 95, "heavysleetshowersandthunder": 97,
		"lightsnowshowersandthunder": 95, "snowshowersandthunder": 95, "heavysnowshowersandthunder": 97,
	}[symbol]
	return code, ok
}
