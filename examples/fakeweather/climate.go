package fakeweather

// climateZone is the package's climate-band enum. Used to look up
// monthly base temperature, seasonal pattern, plausible weather
// conditions, and other downstream correlates.
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

// candidateConditions returns the list of weather conditions that are
// plausible for the given (mean temp, month, zone, seasonal pattern).
// The caller picks one uniformly at random.
func (c climateZone) candidateConditions(temp int, month int, seasonal seasonalPattern) []Condition {
	isSummer := month >= 6 && month <= 8
	isWinter := month == 12 || month <= 2
	isRainy := seasonal.monsoonInfluence && monthInRange(month, seasonal.rainyStart, seasonal.rainyEnd)

	switch c {
	case zoneTropical:
		if isRainy {
			return []Condition{ConditionRainy, ConditionStormy, ConditionPartlyCloudy, ConditionHumid, ConditionDrizzle}
		}
		return []Condition{ConditionPartlyCloudy, ConditionHumid, ConditionSunny, ConditionRainy}

	case zoneDesert:
		if temp > 38 {
			return []Condition{ConditionSunny, ConditionHot, ConditionClear, ConditionDusty, ConditionHazy}
		}
		return []Condition{ConditionSunny, ConditionClear, ConditionPartlyCloudy, ConditionDusty}

	case zoneMediterranean:
		if isSummer {
			return []Condition{ConditionSunny, ConditionClear, ConditionHot, ConditionPartlyCloudy}
		}
		return []Condition{ConditionRainy, ConditionCloudy, ConditionPartlyCloudy, ConditionClear, ConditionDrizzle}

	case zonePolar:
		if temp < -15 {
			return []Condition{ConditionSnowy, ConditionBlizzard, ConditionCloudy, ConditionFreezing, ConditionClear}
		}
		return []Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionOvercast}

	case zoneContinental:
		switch {
		case temp < -5:
			return []Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionBlizzard}
		case temp > 28 && isSummer:
			return []Condition{ConditionSunny, ConditionHot, ConditionStormy, ConditionPartlyCloudy, ConditionClear}
		}
		return []Condition{ConditionSunny, ConditionPartlyCloudy, ConditionCloudy, ConditionClear, ConditionRainy}

	case zoneOceanic:
		if isWinter {
			return []Condition{ConditionRainy, ConditionCloudy, ConditionDrizzle, ConditionOvercast, ConditionFoggy}
		}
		return []Condition{ConditionPartlyCloudy, ConditionCloudy, ConditionSunny, ConditionRainy, ConditionClear}

	case zoneAlpine:
		if temp < 5 {
			return []Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionWindy}
		}
		return []Condition{ConditionPartlyCloudy, ConditionSunny, ConditionClear, ConditionCloudy, ConditionRainy}
	}

	// zoneTemperate (default)
	switch {
	case temp < 0:
		return []Condition{ConditionSnowy, ConditionCloudy, ConditionClear, ConditionCold, ConditionFreezing}
	case temp < 10:
		return []Condition{ConditionCloudy, ConditionClear, ConditionRainy, ConditionFoggy, ConditionDrizzle}
	case temp < 25:
		return []Condition{ConditionSunny, ConditionPartlyCloudy, ConditionCloudy, ConditionClear, ConditionMild}
	}
	if isSummer {
		return []Condition{ConditionSunny, ConditionPartlyCloudy, ConditionRainy, ConditionStormy, ConditionHot}
	}
	return []Condition{ConditionSunny, ConditionHot, ConditionPartlyCloudy, ConditionClear}
}

// seasonalPattern describes a zone's rainfall seasonality. Months are
// 1-based on the *northern hemisphere calendar*; monthForLookup applies
// the southern-hemisphere six-month shift where appropriate.
type seasonalPattern struct {
	rainyStart       int // inclusive (1..12), 0 = no rainy season
	rainyEnd         int // inclusive
	monsoonInfluence bool
	drySeason        bool
}

// climateProfile bundles a zone's monthly mean temperature table plus
// realistic floor/ceiling bounds. Floors prevent jitter+elevation
// from producing impossible values (e.g., a 30°C summer reading
// dropping below 0°C). Index is month-1 (0..11).
type climateProfile struct {
	mean [12]int // monthly mean (°C)
	// dailyAmplitude is the typical Mean→Max swing in °C; Min is
	// symmetric around mean. Day-of-year jitter on top is ±2°C.
	dailyAmplitude int
	// floor and ceiling clamp the final synthesized temperature so
	// jitter+elevation can never produce physically absurd values.
	floor   int // °C lower bound (regardless of month)
	ceiling int // °C upper bound (regardless of month)
}

// climateProfiles is the per-zone table used by every temperature
// derivation. Numbers are deliberately conservative — a synthesized
// "typical" climate, not record extremes.
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
		dailyAmplitude: 12, // characteristic large diurnal swing
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

// seasonalPatterns is the per-zone rainfall pattern. Only zones with
// a meaningful pattern are listed; the default zero value is fine for
// the rest.
var seasonalPatterns = map[climateZone]seasonalPattern{
	zoneTropical:      {rainyStart: 5, rainyEnd: 10, monsoonInfluence: true},
	zoneSubtropical:   {rainyStart: 4, rainyEnd: 9, monsoonInfluence: true},
	zoneMediterranean: {rainyStart: 11, rainyEnd: 3, drySeason: true},
	zoneDesert:        {drySeason: true},
}

// identifyClimateZone returns the zone for the requested location.
// All lookups go through the data tables in cities.go — this
// function holds no city/region names of its own. Order:
//
//  1. Regional patterns ([lookupRegion]) — most specific intent
//     ("antarctica research base" → polar).
//  2. Known cities ([lookupCity]) — gazetteer entries.
//  3. zoneTemperate fallback — also signals "treat as northern
//     hemisphere" downstream so unknown locations retain deterministic
//     seasonal behavior.
func identifyClimateZone(location string) climateZone {
	if zone, ok := lookupRegion(location); ok {
		return zone
	}
	if profile, ok := lookupCity(location); ok {
		return profile.Zone
	}
	return zoneTemperate
}

// monthInRange returns whether month falls within the inclusive
// [start, end] window, handling wrap-around (e.g., Mediterranean
// rainy season Nov..Mar).
func monthInRange(month, start, end int) bool {
	if start == 0 && end == 0 {
		return false
	}
	if start <= end {
		return month >= start && month <= end
	}
	return month >= start || month <= end
}
