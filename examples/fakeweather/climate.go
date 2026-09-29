package fakeweather

import "math"

type climateZone int

const (
	zoneTemperate climateZone = iota
	zoneTropical
	zoneSubtropical
	zoneContinental
	zonePolar
	zoneDesert
	zoneMediterranean
	zoneOceanic
	zoneAlpine
)

// Months use the northern hemisphere calendar; monthForLookup shifts known
// southern locations by six months.
func (c climateZone) candidateConditions(temp int, month int) []Condition {
	season := conditionSeason{
		temperature: temp,
		summer:      month >= 6 && month <= 8,
		winter:      month == 12 || month <= 2,
		monsoon:     c.monsoon().covers(month),
	}
	for _, rule := range zoneConditionRules[c] {
		if rule.applies == nil || rule.applies(season) {
			return rule.conditions
		}
	}
	panic("fakeweather: every climate zone needs condition rules ending with an unconditional rule")
}

func (c climateZone) monsoon() monsoonSeason {
	switch c {
	case zoneTropical:
		return monsoonSeason{start: 5, end: 10}
	case zoneSubtropical:
		return monsoonSeason{start: 4, end: 9}
	}
	return monsoonSeason{}
}

func (c climateZone) baseHumidity(month int) int {
	switch c {
	case zoneTropical:
		if c.monsoon().covers(month) {
			return 85
		}
		return 75
	case zoneDesert:
		return 20
	case zoneMediterranean:
		if month >= 6 && month <= 9 {
			return 45
		}
		return 65
	case zonePolar:
		return 70
	case zoneOceanic:
		return 75
	case zoneAlpine:
		return 60
	case zoneContinental:
		return 55
	}
	return 50
}

func (c climateZone) typhoonSeason(month int) bool {
	return (c == zoneTropical || c == zoneSubtropical) && month >= 6 && month <= 10
}

type conditionSeason struct {
	temperature int
	summer      bool
	winter      bool
	monsoon     bool
}

// A nil predicate always applies; each rule list ends with one.
type conditionRule struct {
	applies    func(conditionSeason) bool
	conditions []Condition
}

var temperateConditionRules = []conditionRule{
	{
		func(s conditionSeason) bool { return s.temperature < 0 },
		[]Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionFreezing},
	},
	{
		func(s conditionSeason) bool { return s.temperature < 10 },
		[]Condition{ConditionCloudy, ConditionClear, ConditionRainy, ConditionFoggy, ConditionDrizzle},
	},
	{
		func(s conditionSeason) bool { return s.temperature < 25 },
		[]Condition{ConditionSunny, ConditionPartlyCloudy, ConditionCloudy, ConditionClear, ConditionMild},
	},
	{
		func(s conditionSeason) bool { return s.summer },
		[]Condition{ConditionSunny, ConditionPartlyCloudy, ConditionRainy, ConditionStormy, ConditionHot},
	},
	{nil, []Condition{ConditionSunny, ConditionHot, ConditionPartlyCloudy, ConditionClear}},
}

var zoneConditionRules = map[climateZone][]conditionRule{
	zoneTemperate:   temperateConditionRules,
	zoneSubtropical: temperateConditionRules,
	zoneTropical: {
		{
			func(s conditionSeason) bool { return s.monsoon },
			[]Condition{ConditionRainy, ConditionStormy, ConditionPartlyCloudy, ConditionHumid, ConditionDrizzle},
		},
		{nil, []Condition{ConditionPartlyCloudy, ConditionHumid, ConditionSunny, ConditionRainy}},
	},
	zoneDesert: {
		{
			func(s conditionSeason) bool { return s.temperature > 38 },
			[]Condition{ConditionSunny, ConditionHot, ConditionClear, ConditionDusty, ConditionHazy},
		},
		{nil, []Condition{ConditionSunny, ConditionClear, ConditionPartlyCloudy, ConditionDusty}},
	},
	zoneMediterranean: {
		{
			func(s conditionSeason) bool { return s.summer },
			[]Condition{ConditionSunny, ConditionClear, ConditionHot, ConditionPartlyCloudy},
		},
		{nil, []Condition{ConditionRainy, ConditionCloudy, ConditionPartlyCloudy, ConditionClear, ConditionDrizzle}},
	},
	zonePolar: {
		{
			func(s conditionSeason) bool { return s.temperature < -15 },
			[]Condition{ConditionSnowy, ConditionBlizzard, ConditionCloudy, ConditionFreezing, ConditionClear},
		},
		{nil, []Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionOvercast}},
	},
	zoneContinental: {
		{
			func(s conditionSeason) bool { return s.temperature < -5 },
			[]Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionBlizzard},
		},
		{
			func(s conditionSeason) bool { return s.temperature > 28 && s.summer },
			[]Condition{ConditionSunny, ConditionHot, ConditionStormy, ConditionPartlyCloudy, ConditionClear},
		},
		{nil, []Condition{ConditionSunny, ConditionPartlyCloudy, ConditionCloudy, ConditionClear, ConditionRainy}},
	},
	zoneOceanic: {
		{
			func(s conditionSeason) bool { return s.winter },
			[]Condition{ConditionRainy, ConditionCloudy, ConditionDrizzle, ConditionOvercast, ConditionFoggy},
		},
		{nil, []Condition{ConditionPartlyCloudy, ConditionCloudy, ConditionSunny, ConditionRainy, ConditionClear}},
	},
	zoneAlpine: {
		{
			func(s conditionSeason) bool { return s.temperature < 5 },
			[]Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionWindy},
		},
		{nil, []Condition{ConditionPartlyCloudy, ConditionSunny, ConditionClear, ConditionCloudy, ConditionRainy}},
	},
}

// The zero value has no monsoon; a season may cross December.
type monsoonSeason struct {
	start int
	end   int
}

func (m monsoonSeason) covers(month int) bool {
	if m == (monsoonSeason{}) {
		return false
	}
	if m.start <= m.end {
		return month >= m.start && month <= m.end
	}
	return month >= m.start || month <= m.end
}

type climateProfile struct {
	mean           [12]int // Celsius
	dailyAmplitude int     // Celsius swing from mean to maximum
	floor          int     // Celsius
	ceiling        int     // Celsius
}

func (c climateProfile) dailyVariation(hour int) int {
	const peakHour = 14
	return int(math.Round(float64(c.dailyAmplitude) * math.Cos(float64(hour-peakHour)*math.Pi/12)))
}

var climateProfiles = map[climateZone]climateProfile{
	zoneTemperate: {
		mean:           [12]int{5, 7, 12, 18, 23, 28, 30, 29, 24, 18, 12, 7},
		dailyAmplitude: 6,
		floor:          -15, ceiling: 40,
	},
	zoneTropical: {
		mean:           [12]int{27, 27, 28, 29, 29, 28, 28, 28, 28, 28, 27, 27},
		dailyAmplitude: 4,
		floor:          18, ceiling: 38,
	},
	zoneSubtropical: {
		mean:           [12]int{10, 12, 16, 22, 26, 30, 32, 31, 28, 22, 16, 11},
		dailyAmplitude: 6,
		floor:          -5, ceiling: 40,
	},
	zoneContinental: {
		mean:           [12]int{-5, -2, 5, 14, 21, 26, 28, 26, 20, 12, 3, -3},
		dailyAmplitude: 8,
		floor:          -35, ceiling: 38,
	},
	zonePolar: {
		mean:           [12]int{-25, -22, -15, -8, -2, 3, 5, 4, -1, -10, -18, -23},
		dailyAmplitude: 4,
		floor:          -55, ceiling: 12,
	},
	zoneDesert: {
		mean:           [12]int{15, 18, 22, 28, 35, 40, 42, 41, 37, 30, 22, 16},
		dailyAmplitude: 12,
		floor:          0, ceiling: 50,
	},
	zoneMediterranean: {
		mean:           [12]int{12, 13, 15, 18, 22, 27, 30, 30, 26, 21, 16, 13},
		dailyAmplitude: 7,
		floor:          -5, ceiling: 42,
	},
	zoneOceanic: {
		mean:           [12]int{7, 8, 10, 13, 16, 19, 21, 21, 18, 14, 10, 8},
		dailyAmplitude: 5,
		floor:          -10, ceiling: 32,
	},
	zoneAlpine: {
		mean:           [12]int{-5, -3, 2, 8, 13, 17, 19, 18, 14, 9, 2, -3},
		dailyAmplitude: 8,
		floor:          -25, ceiling: 28,
	},
}

// Regional hints take precedence over city climate profiles.
func identifyClimateZone(location string) climateZone {
	if zone, ok := regionalZones.lookup(location); ok {
		return zone
	}
	if profile, ok := knownCities.lookup(location); ok {
		return profile.Zone
	}
	return zoneTemperate
}
