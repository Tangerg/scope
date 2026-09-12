package fakeweather

import (
	"math"
)

func directionFromDegree(deg int) string {
	directions := []string{
		"North", "North-North-East", "North-East", "East-North-East",
		"East", "East-South-East", "South-East", "South-South-East",
		"South", "South-South-West", "South-West", "West-South-West",
		"West", "West-North-West", "North-West", "North-North-West",
	}
	return directions[int(math.Round(float64(deg)/22.5))%16]
}

// calculateFeelsLike applies wind-chill (cold + windy) and heat-index
// (hot + humid) corrections; falls back to the raw temp otherwise.
func calculateFeelsLike(temp, humidity int, windSpeed float64) int {
	t := float64(temp)
	feels := t

	if temp < 10 && windSpeed > 4.8 {
		feels = 13.12 + 0.6215*t - 11.37*math.Pow(windSpeed, 0.16) +
			0.3965*t*math.Pow(windSpeed, 0.16)
	}

	if temp > 27 && humidity > 40 {
		rh := float64(humidity)
		feels = -8.78469475556 + 1.61139411*t + 2.33854883889*rh -
			0.14611605*t*rh - 0.012308094*t*t - 0.0164248277778*rh*rh +
			0.002211732*t*t*rh + 0.00072546*t*rh*rh - 0.000003582*t*t*rh*rh
	}

	return int(math.Round(feels))
}

// calculateDewPoint applies the Magnus formula. Returns the dew point
// in °C, rounded to int.
func calculateDewPoint(temp, humidity int) int {
	const a = 17.27
	const b = 237.7
	t := float64(temp)
	rh := float64(humidity) / 100.0
	if rh <= 0 {
		return temp
	}
	alpha := (a*t)/(b+t) + math.Log(rh)
	return int(math.Round((b * alpha) / (a - alpha)))
}

var moonPhases = []string{
	"New Moon", "Waxing Crescent", "First Quarter", "Waxing Gibbous",
	"Full Moon", "Waning Gibbous", "Last Quarter", "Waning Crescent",
}
