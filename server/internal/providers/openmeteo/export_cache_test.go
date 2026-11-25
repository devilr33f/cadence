package openmeteo

import "git.shino.boo/devilreef/cadence/server/internal/providers"

// CacheLen reports the geocode cache size.
func CacheLen(w providers.Weather) int { return w.(*weather).geocoder.cache.len() }
