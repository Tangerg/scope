package fakeweather

import (
	"fmt"
	"math"
	"strings"
)

func formatHM(hours float64) string {
	if math.IsNaN(hours) || math.IsInf(hours, 0) {
		return "--:--"
	}
	h := int(math.Floor(hours))
	m := int((hours - math.Floor(hours)) * 60)
	if h < 0 {
		h += 24
	}
	h %= 24
	if m < 0 {
		m += 60
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

func buildDescription(condition Condition, temp int, wind Wind, humidity int, precip *Precipitation) string {
	var b strings.Builder
	b.WriteString(condition.summary(precip))

	switch {
	case temp < 0:
		b.WriteString(" Freezing temperatures.")
	case temp > 30:
		b.WriteString(" High temperatures.")
	}

	switch {
	case wind.Speed > 30:
		fmt.Fprintf(&b, " Strong winds from the %s at %.1f km/h.", strings.ToLower(wind.Direction), wind.Speed)
	case wind.Speed > 15:
		fmt.Fprintf(&b, " Moderate winds from the %s.", strings.ToLower(wind.Direction))
	}

	switch {
	case humidity > 80:
		b.WriteString(" High humidity making it feel muggy.")
	case humidity < 30:
		b.WriteString(" Low humidity with dry air.")
	}
	return b.String()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
