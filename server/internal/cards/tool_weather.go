package cards

import (
	"math"
	"strconv"
	"strings"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

type weatherDay struct {
	Date            string   `json:"date"`
	Min             float64  `json:"min"`
	Max             float64  `json:"max"`
	PrecipitationMm *float64 `json:"precipitationMm"`
	Code            int      `json:"code"`
	Summary         string   `json:"summary"`
}

// weatherCards: one day as its figures, several as a column a day
func weatherCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Place string       `json:"place"`
		Days  []weatherDay `json:"days"`
	}
	if !decode(r, &out) || len(out.Days) == 0 {
		return nil
	}
	days := out.Days[:min(len(out.Days), 4)]
	return card("weather", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		first := days[0]
		b := &cardsv1.CardBlock{Title: clip(rr.T("weather.title", "place", clip(out.Place, 60)), 80),
			Subtitle: clip(first.Summary, 80), Icon: weatherIcon(first.Code), Tone: cardsv1.Tone_TONE_NEUTRAL}
		if len(days) == 1 {
			b.Children = pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_L, Children: pcards.Nodes(
				&cardsv1.Icon{Name: weatherIcon(first.Code), Size: 48},
				&cardsv1.Value{Value: degrees(first.Max), Unit: "°C", Label: rr.T("weather.max")},
				&cardsv1.Value{Value: degrees(first.Min), Unit: "°C", Label: rr.T("weather.min")},
			)})
			if p := first.PrecipitationMm; p != nil {
				b.Children = append(b.Children, pcards.N(rows([][2]string{
					{rr.T("weather.precipitation"), decimal(*p, rr.Lang) + " " + rr.T("weather.mm")},
				})))
			}
		} else {
			g := &cardsv1.Grid{Gap: cardsv1.Gap_GAP_M}
			for _, d := range days {
				g.Columns++
				g.Children = append(g.Children, pcards.N(&cardsv1.Column{Gap: cardsv1.Gap_GAP_S, Children: pcards.Nodes(
					&cardsv1.Text{Text: dayLabel(d.Date, rr.Lang), Style: cardsv1.TextStyle_TEXT_STYLE_META},
					&cardsv1.Icon{Name: weatherIcon(d.Code), Size: 32},
					&cardsv1.Value{Value: degrees(d.Min) + "…" + degrees(d.Max), Unit: "°C"},
				)}))
			}
			b.Children = pcards.Nodes(g)
		}
		fallback := joined(": ", clip(out.Place, 80), joined(", ", clip(first.Summary, 80),
			degrees(first.Min)+"…"+degrees(first.Max)+" °C"))
		return b, fallback
	})
}

// decimal is a figure with the language's decimal mark
func decimal(v float64, lang string) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if strings.HasPrefix(strings.ToLower(lang), "ru") {
		return strings.ReplaceAll(s, ".", ",")
	}
	return s
}

// degrees is a whole figure with its sign, as the weather fixture writes it
func degrees(v float64) string {
	n := int(math.Round(v))
	if n > 0 {
		return "+" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

var (
	weekdaysRU = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
	weekdaysEN = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
)

// dayLabel is a forecast date as "пт, 14" / "Fri 14", the date itself when it does not parse
func dayLabel(date, lang string) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return clip(date, 40)
	}
	if strings.HasPrefix(strings.ToLower(lang), "ru") {
		return weekdaysRU[t.Weekday()] + ", " + strconv.Itoa(t.Day())
	}
	return weekdaysEN[t.Weekday()] + " " + strconv.Itoa(t.Day())
}

// weatherIcon is a WMO weather code's icon of the screen's set
func weatherIcon(code int) string {
	switch {
	case code <= 1:
		return "sun"
	case code <= 3:
		return "cloud"
	case code == 45 || code == 48:
		return "fog"
	case code >= 71 && code <= 77, code == 85 || code == 86:
		return "snow"
	case code >= 95:
		return "storm"
	case code >= 51:
		return "rain"
	}
	return "cloud"
}
