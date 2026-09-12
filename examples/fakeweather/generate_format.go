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

	switch condition {
	case ConditionSunny:
		b.WriteString("Clear skies with abundant sunshine throughout the day.")
	case ConditionPartlyCloudy:
		b.WriteString("Mix of sun and clouds with pleasant weather conditions.")
	case ConditionCloudy:
		b.WriteString("Overcast skies with cloud cover throughout the day.")
	case ConditionRainy:
		b.WriteString("Rainy conditions expected.")
		if precip != nil {
			fmt.Fprintf(&b, " Rainfall amount: %.1f mm. %s intensity.", precip.Amount, precip.Intensity)
		}
	case ConditionStormy:
		b.WriteString("Severe thunderstorms with heavy rain and strong winds. Lightning activity expected.")
	case ConditionSnowy:
		b.WriteString("Snow is expected.")
		if precip != nil {
			fmt.Fprintf(&b, " Snowfall amount: %.1f mm. %s intensity.", precip.Amount, precip.Intensity)
		}
	case ConditionBlizzard:
		b.WriteString("Blizzard conditions with heavy snow and very strong winds. Visibility severely reduced.")
	case ConditionFoggy:
		b.WriteString("Dense fog reducing visibility significantly. Drive with caution.")
	case ConditionHot:
		b.WriteString("Hot and sunny conditions. Take precautions against heat.")
	default:
		fmt.Fprintf(&b, "%s weather conditions expected.", condition)
	}

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
