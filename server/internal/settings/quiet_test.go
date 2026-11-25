package settings_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestQuietHoursWrapAndTimeZone(t *testing.T) {
	night := settings.QuietHours{Enabled: true, Start: "23:00", End: "08:00"}
	day := settings.QuietHours{Enabled: true, Start: "13:00", End: "15:30"}
	at := func(hhmm string) time.Time {
		tm, err := time.Parse("2006-01-02 15:04", "2026-11-09 "+hhmm)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		q     settings.QuietHours
		utc   string
		zone  string
		quiet bool
	}{
		{night, "23:00", "UTC", true},
		{night, "22:59", "UTC", false},
		{night, "03:00", "UTC", true},
		{night, "07:59", "UTC", true},
		{night, "08:00", "UTC", false},
		{night, "12:00", "UTC", false},
		{day, "13:00", "UTC", true},
		{day, "15:29", "UTC", true},
		{day, "15:30", "UTC", false},
		{day, "02:00", "UTC", false},
		// Bishkek is UTC+6: 17:30 UTC is 23:30 there, 02:30 UTC is 08:30
		{night, "17:30", "Asia/Bishkek", true},
		{night, "02:30", "Asia/Bishkek", false},
		{night, "01:30", "Asia/Bishkek", true},
		// a zone that does not load reads the UTC clock
		{night, "23:30", "Not/AZone", true},
		{settings.QuietHours{Start: "23:00", End: "08:00"}, "23:30", "UTC", false},
		{settings.QuietHours{Enabled: true, Start: "bad", End: "08:00"}, "03:00", "UTC", false},
	}
	for _, c := range cases {
		if got := c.q.Quiet(at(c.utc), c.zone); got != c.quiet {
			t.Errorf("%+v at %s UTC in %s: %v", c.q, c.utc, c.zone, got)
		}
	}
}
