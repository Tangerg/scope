package fakeweather

import (
	"math"
	"time"
)

const (
	syntheticLunarCycleDays = 29.5
	// The synthetic cycle starts at a new moon on 2000-01-01 UTC.
	syntheticNewMoonUnix = 946684800
)

var moonPhases = [...]string{
	"New Moon", "Waxing Crescent", "First Quarter", "Waxing Gibbous",
	"Full Moon", "Waning Gibbous", "Last Quarter", "Waning Crescent",
}

// lunarPhase is the fraction of a synthetic cycle since the reference new moon.
type lunarPhase float64

func newLunarPhase(date time.Time) lunarPhase {
	const secondsPerDay = hoursPerDay * 60 * 60
	phase := math.Mod(float64(date.Unix()-syntheticNewMoonUnix)/(secondsPerDay*syntheticLunarCycleDays), 1)
	if phase < 0 {
		phase++
	}
	return lunarPhase(phase)
}

func (l lunarPhase) name() string {
	index := int(math.Round(float64(l)*float64(len(moonPhases)))) % len(moonPhases)
	return moonPhases[index]
}

func (l lunarPhase) illumination() int {
	return int(math.Round((1 - math.Cos(2*math.Pi*float64(l))) / 2 * fullPercentage))
}
