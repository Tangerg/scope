package fakeweather

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	minutesPerHour           = int(time.Hour / time.Minute)
	measurementDecimalPlaces = 1
)

func roundMeasurement(value float64) float64 {
	scale := math.Pow10(measurementDecimalPlaces)
	return math.Round(value*scale) / scale
}

func formatHM(hours float64) string {
	const unavailableTime = "--:--"
	if math.IsNaN(hours) || math.IsInf(hours, 0) {
		return unavailableTime
	}
	h := int(math.Floor(hours))
	m := int((hours - math.Floor(hours)) * float64(minutesPerHour))
	if h < 0 {
		h += hoursPerDay
	}
	h %= hoursPerDay
	if m < 0 {
		m += minutesPerHour
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

func buildDescription(condition Condition, temp int, wind Wind, humidity int, precip *Precipitation) string {
	const (
		hotDescriptionThresholdCelsius      = 30
		strongWindDescriptionThresholdKPH   = 30
		moderateWindDescriptionThresholdKPH = 15
		highHumidityDescriptionThreshold    = 80
		lowHumidityDescriptionThreshold     = 30
	)
	var b strings.Builder
	b.WriteString(condition.summary())
	b.WriteString(precip.amountSentence())

	switch {
	case temp < freezingPointCelsius:
		b.WriteString(" Freezing temperatures.")
	case temp > hotDescriptionThresholdCelsius:
		b.WriteString(" High temperatures.")
	}

	switch {
	case wind.Speed > strongWindDescriptionThresholdKPH:
		fmt.Fprintf(&b, " Strong winds from the %s at %.*f %s.", strings.ToLower(wind.Direction), measurementDecimalPlaces, wind.Speed, windSpeedUnit)
	case wind.Speed > moderateWindDescriptionThresholdKPH:
		fmt.Fprintf(&b, " Moderate winds from the %s.", strings.ToLower(wind.Direction))
	}

	switch {
	case humidity > highHumidityDescriptionThreshold:
		b.WriteString(" High humidity making it feel muggy.")
	case humidity < lowHumidityDescriptionThreshold:
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
