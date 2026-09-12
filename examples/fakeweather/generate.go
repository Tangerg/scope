package fakeweather

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

type reportGenerator struct {
	request  Request
	target   time.Time
	rng      *rand.Rand
	zone     climateZone
	coords   Coordinates
	seasonal seasonalPattern
	profile  climateProfile
	month    int
}

// generate is the entry point: parse the request, derive every output
// field deterministically from (location, date), and return the
// Response. All randomness is seeded from the input — same input,
// same output across runs.
func generate(req *Request) (*Response, error) {
	generator, err := newReportGenerator(req)
	if err != nil {
		return nil, fmt.Errorf("fakeweather.generate: %w", err)
	}
	return generator.report(), nil
}

func newReportGenerator(req *Request) (*reportGenerator, error) {
	target, err := parseTargetDate(req.Date)
	if err != nil {
		return nil, err
	}

	rng := newRng(req.Location, target)
	zone := identifyClimateZone(req.Location)
	coords, knownCity := coordinatesFor(req.Location, rng)
	return &reportGenerator{
		request:  *req,
		target:   target,
		rng:      rng,
		zone:     zone,
		coords:   coords,
		seasonal: seasonalPatterns[zone],
		profile:  climateProfiles[zone],
		month:    monthForLookup(target, coords.Latitude, knownCity),
	}, nil
}

func (r *reportGenerator) report() *Response {
	// Daily mean for the (zone, month). For a date-only query this
	// IS the day's representative reading — diurnal variation is deliberately
	// NOT applied, because that would put every
	// midnight-stamped query at the bottom of the daily curve.
	mean := r.profile.mean[r.month-1]

	// Elevation correction: ~0.6°C drop per 100 m.
	elevDrop := int(float64(r.coords.Elevation) * 0.006)
	mean -= elevDrop

	// Day-to-day jitter (±2°C) preserves variability without
	// breaking the seasonal floor.
	jitter := r.rng.IntN(5) - 2
	current := clamp(mean+jitter, r.profile.floor, r.profile.ceiling)

	// Min/Max describe the whole day's swing around the mean — not
	// random offsets from the "current" reading.
	minTemp := min(clamp(mean-r.profile.dailyAmplitude+r.rng.IntN(3)-1, r.profile.floor, r.profile.ceiling), current)
	maxTemp := max(clamp(mean+r.profile.dailyAmplitude+r.rng.IntN(3)-1, r.profile.floor, r.profile.ceiling), current)

	// Pick a condition compatible with the temperature + zone + month.
	candidates := candidateConditions(current, r.month, r.zone, r.seasonal)
	condition := candidates[r.rng.IntN(len(candidates))]

	wind := r.wind(condition)
	humidity := r.humidity(condition)
	feelsLike := calculateFeelsLike(current, humidity, wind.Speed)
	pressure := r.pressure(condition)
	visibility := r.visibility(condition, humidity)
	cloudCover := r.cloudCover(condition)
	dewPoint := calculateDewPoint(current, humidity)

	var precipitation *Precipitation
	if condition.hasPrecipitation() {
		precipitation = r.precipitation(condition, current)
	}

	var airQuality *AirQuality
	if r.request.IncludeAirQuality {
		airQuality = r.airQuality(condition)
	}

	uvIndex := r.uvIndex(condition, cloudCover)
	astronomy := r.astronomy()

	var hourlyForecast []HourlyForecast
	if r.request.IncludeHourly {
		hourlyForecast = r.hourlyForecast(mean, condition)
	}

	alerts := r.alerts(condition, current, wind.Speed)
	description := buildDescription(condition, current, wind, humidity, precipitation)

	startOfDay := time.Date(r.target.Year(), r.target.Month(), r.target.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	return &Response{
		Location:    r.request.Location,
		Coordinates: r.coords,
		Timestamp: TimeRange{
			Start: startOfDay.Unix(),
			End:   endOfDay.Unix(),
		},
		Temperature: Temperature{
			Value:     current,
			Unit:      "Celsius",
			FeelsLike: feelsLike,
			Min:       minTemp,
			Max:       maxTemp,
		},
		Condition:      condition,
		Description:    description,
		Humidity:       humidity,
		Pressure:       pressure,
		Visibility:     visibility,
		CloudCover:     cloudCover,
		DewPoint:       dewPoint,
		Wind:           wind,
		Precipitation:  precipitation,
		AirQuality:     airQuality,
		UVIndex:        uvIndex,
		Astronomy:      astronomy,
		HourlyForecast: hourlyForecast,
		Alerts:         alerts,
		Source:         "fakeweather (synthesized; not real weather data)",
		LastUpdated:    startOfDay.Unix(),
	}
}

func (r *reportGenerator) hourlyForecast(dailyMean int, condition Condition) []HourlyForecast {
	out := make([]HourlyForecast, 24)
	for i := range 24 {
		hour := time.Date(r.target.Year(), r.target.Month(), r.target.Day(), i, 0, 0, 0, time.UTC)

		// Sinusoidal diurnal cycle: hottest at 14:00, coolest at 02:00.
		amp := r.profile.dailyAmplitude
		if r.zone == zoneDesert {
			amp = 12
		}
		variation := int(math.Round(float64(amp) * math.Sin(float64(i-2)*math.Pi/12)))
		hourTemp := clamp(dailyMean+variation+r.rng.IntN(3)-1, r.profile.floor, r.profile.ceiling)

		hourCondition := condition
		if r.rng.Float64() < 0.2 {
			alt := candidateConditions(hourTemp, int(r.target.Month()), r.zone, seasonalPattern{})
			hourCondition = alt[r.rng.IntN(len(alt))]
		}

		precip := 0.0
		if hourCondition.hasPrecipitation() {
			precip = math.Round(r.rng.Float64()*5.0*10) / 10
		}

		humidity := 50 + r.rng.IntN(30)
		if i >= 22 || i <= 6 {
			humidity = min(humidity+10, 100)
		}

		out[i] = HourlyForecast{
			Time:          hour.Unix(),
			Temperature:   hourTemp,
			Condition:     hourCondition,
			Precipitation: precip,
			Humidity:      humidity,
			WindSpeed:     math.Round((5.0+r.rng.Float64()*15.0)*10) / 10,
		}
	}
	return out
}

func (r *reportGenerator) alerts(condition Condition, temp int, windSpeed float64) []Alert {
	var alerts []Alert
	day := r.target.Add(24 * time.Hour)

	if temp >= 35 {
		severity := AlertSeverityModerate
		if temp >= 40 {
			severity = AlertSeveritySevere
		}
		alerts = append(alerts, Alert{
			Type:        AlertHeat,
			Severity:    severity,
			Title:       "High Temperature Warning",
			Description: fmt.Sprintf("Temperature is expected to reach %d°C. Stay hydrated and avoid prolonged sun exposure.", temp),
			StartTime:   r.target.Unix(),
			EndTime:     day.Unix(),
		})
	}
	if temp <= -10 {
		severity := AlertSeverityModerate
		if temp <= -20 {
			severity = AlertSeveritySevere
		}
		alerts = append(alerts, Alert{
			Type:        AlertCold,
			Severity:    severity,
			Title:       "Extreme Cold Warning",
			Description: fmt.Sprintf("Temperature is expected to drop to %d°C. Dress warmly and limit outdoor exposure.", temp),
			StartTime:   r.target.Unix(),
			EndTime:     day.Unix(),
		})
	}
	if windSpeed >= 50 {
		severity := AlertSeverityModerate
		if windSpeed >= 70 {
			severity = AlertSeveritySevere
		}
		alerts = append(alerts, Alert{
			Type:        AlertWind,
			Severity:    severity,
			Title:       "High Wind Warning",
			Description: fmt.Sprintf("Wind speeds may reach %.1f km/h. Secure loose objects and avoid outdoor activities.", windSpeed),
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(12 * time.Hour).Unix(),
		})
	}
	if condition == ConditionStormy {
		alerts = append(alerts, Alert{
			Type:        AlertStorm,
			Severity:    AlertSeveritySevere,
			Title:       "Severe Storm Warning",
			Description: "Severe thunderstorms expected. Stay indoors and avoid travel if possible.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(6 * time.Hour).Unix(),
		})
	}
	if condition == ConditionBlizzard {
		alerts = append(alerts, Alert{
			Type:        AlertSnow,
			Severity:    AlertSeveritySevere,
			Title:       "Blizzard Warning",
			Description: "Blizzard conditions expected with heavy snow and strong winds. Travel is strongly discouraged.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(12 * time.Hour).Unix(),
		})
	}
	month := int(r.target.Month())
	if (r.zone == zoneTropical || r.zone == zoneSubtropical) && month >= 6 && month <= 10 && r.rng.Float64() < 0.05 {
		alerts = append(alerts, Alert{
			Type:        AlertTyphoon,
			Severity:    AlertSeverityExtreme,
			Title:       "Typhoon Warning",
			Description: "A typhoon is approaching. Evacuate if instructed by authorities and prepare for extreme weather.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(48 * time.Hour).Unix(),
		})
	}
	return alerts
}

func (r *reportGenerator) wind(condition Condition) Wind {
	speed := 5.0 + r.rng.Float64()*15.0
	switch condition {
	case ConditionStormy, ConditionBlizzard:
		speed += r.rng.Float64() * 30.0
	case ConditionRainy, ConditionSnowy:
		speed += r.rng.Float64() * 15.0
	case ConditionSunny, ConditionClear:
		speed *= 0.6
	}
	switch r.zone {
	case zoneDesert:
		speed += r.rng.Float64() * 10.0
	case zoneOceanic:
		speed += r.rng.Float64() * 8.0
	case zoneAlpine:
		speed += r.rng.Float64() * 12.0
	}
	speed += float64(r.coords.Elevation) * 0.01

	degree := r.rng.IntN(360)
	gust := speed * (1.2 + r.rng.Float64()*0.3)
	return Wind{
		Speed:     math.Round(speed*10) / 10,
		Unit:      "km/h",
		Direction: directionFromDegree(degree),
		Degree:    degree,
		Gust:      math.Round(gust*10) / 10,
	}
}

// humidity follows the zone's typical humidity, lifted by
// rainy/foggy conditions and reduced by sunny/dusty ones.
func (r *reportGenerator) humidity(condition Condition) int {
	base := 50
	switch r.zone {
	case zoneTropical:
		base = 75
		if r.seasonal.monsoonInfluence && monthInRange(r.month, r.seasonal.rainyStart, r.seasonal.rainyEnd) {
			base = 85
		}
	case zoneDesert:
		base = 20
	case zoneMediterranean:
		if r.month >= 6 && r.month <= 9 {
			base = 45
		} else {
			base = 65
		}
	case zonePolar:
		base = 70
	case zoneOceanic:
		base = 75
	case zoneAlpine:
		base = 60
	case zoneContinental:
		base = 55
	}

	switch condition {
	case ConditionRainy, ConditionStormy, ConditionFoggy, ConditionHumid, ConditionDrizzle:
		return min(base+20+r.rng.IntN(20), 100)
	case ConditionSnowy, ConditionBlizzard:
		return min(base+15+r.rng.IntN(15), 95)
	case ConditionCloudy, ConditionPartlyCloudy, ConditionOvercast:
		return base + r.rng.IntN(15)
	case ConditionSunny, ConditionClear, ConditionHot:
		return max(base-20+r.rng.IntN(20), 10)
	case ConditionDusty, ConditionHazy:
		return max(base-30+r.rng.IntN(15), 5)
	}
	return base + r.rng.IntN(20) - 10
}

// pressure starts from the elevation-corrected MSL pressure and
// adjusts for the weather (low for storms, high for clear).
func (r *reportGenerator) pressure(condition Condition) int {
	base := 1013 - r.coords.Elevation/8
	switch condition {
	case ConditionStormy, ConditionRainy:
		base += -10 - r.rng.IntN(15)
	case ConditionSunny, ConditionClear:
		base += 5 + r.rng.IntN(10)
	case ConditionCloudy, ConditionPartlyCloudy:
		base += r.rng.IntN(10) - 5
	}
	return base
}

func (r *reportGenerator) visibility(condition Condition, humidity int) int {
	var base int
	switch condition {
	case ConditionFoggy:
		base = r.rng.IntN(2) + 1
	case ConditionRainy, ConditionSnowy:
		base = 3 + r.rng.IntN(5)
	case ConditionStormy, ConditionBlizzard:
		base = 1 + r.rng.IntN(3)
	case ConditionDusty, ConditionHazy:
		base = 2 + r.rng.IntN(6)
	case ConditionCloudy:
		base = 8 + r.rng.IntN(7)
	case ConditionSunny, ConditionClear:
		base = 15 + r.rng.IntN(35)
	default:
		base = 10 + r.rng.IntN(10)
	}
	if humidity > 85 {
		base = int(float64(base) * 0.7)
	}
	return max(1, base)
}

func (r *reportGenerator) cloudCover(condition Condition) int {
	switch condition {
	case ConditionSunny, ConditionClear:
		return r.rng.IntN(15)
	case ConditionPartlyCloudy:
		return 25 + r.rng.IntN(35)
	case ConditionCloudy, ConditionOvercast:
		return 75 + r.rng.IntN(25)
	case ConditionRainy, ConditionSnowy, ConditionStormy:
		return 90 + r.rng.IntN(10)
	case ConditionFoggy:
		return 100
	}
	return 40 + r.rng.IntN(40)
}

func (r *reportGenerator) precipitation(condition Condition, temp int) *Precipitation {
	p := &Precipitation{}
	switch {
	case temp < 0:
		p.Type = PrecipitationSnow
	case temp < 3:
		if r.rng.Float64() < 0.3 {
			p.Type = PrecipitationSleet
		} else {
			p.Type = PrecipitationSnow
		}
	default:
		p.Type = PrecipitationRain
	}

	switch condition {
	case ConditionStormy, ConditionBlizzard:
		p.Probability = 85 + r.rng.IntN(15)
	case ConditionRainy, ConditionSnowy:
		p.Probability = 60 + r.rng.IntN(30)
	case ConditionDrizzle:
		p.Probability = 40 + r.rng.IntN(30)
	default:
		p.Probability = 30 + r.rng.IntN(40)
	}
	if r.seasonal.monsoonInfluence && monthInRange(r.month, r.seasonal.rainyStart, r.seasonal.rainyEnd) {
		p.Probability = min(100, p.Probability+15)
	}

	switch condition {
	case ConditionStormy:
		p.Amount = 20.0 + r.rng.Float64()*40.0
		p.Intensity = PrecipitationHeavy
	case ConditionRainy:
		p.Amount = 5.0 + r.rng.Float64()*20.0
		if p.Amount > 15 {
			p.Intensity = PrecipitationModerate
		} else {
			p.Intensity = PrecipitationLight
		}
	case ConditionDrizzle:
		p.Amount = 0.5 + r.rng.Float64()*3.0
		p.Intensity = PrecipitationLight
	case ConditionSnowy, ConditionBlizzard:
		p.Amount = 1.0 + r.rng.Float64()*10.0
		if condition == ConditionBlizzard {
			p.Intensity = PrecipitationHeavy
		} else {
			p.Intensity = PrecipitationModerate
		}
	default:
		p.Amount = r.rng.Float64() * 5.0
		p.Intensity = PrecipitationLight
	}
	p.Amount = math.Round(p.Amount*10) / 10
	return p
}

func (r *reportGenerator) airQuality(condition Condition) *AirQuality {
	aq := &AirQuality{}
	aqi := 50

	if profile, ok := lookupCity(r.request.Location); ok && profile.Polluted {
		aqi = 80 + r.rng.IntN(40)
	}

	switch condition {
	case ConditionFoggy, ConditionHazy:
		aqi += 40 + r.rng.IntN(30)
	case ConditionRainy, ConditionStormy:
		aqi -= 20 + r.rng.IntN(20)
	case ConditionWindy:
		aqi -= 10 + r.rng.IntN(15)
	}
	if r.zone == zoneDesert {
		aqi += 10 + r.rng.IntN(20)
	}

	aq.AQI = clamp(aqi, 0, 500)
	switch {
	case aq.AQI <= 50:
		aq.Level = AirQualityGood
		aq.Description = "Air quality is satisfactory, and air pollution poses little or no risk."
	case aq.AQI <= 100:
		aq.Level = AirQualityModerate
		aq.Description = "Air quality is acceptable. There may be a risk for some people sensitive to air pollution."
	case aq.AQI <= 150:
		aq.Level = AirQualityUnhealthyForSensitiveGroups
		aq.Description = "Members of sensitive groups may experience health effects."
	case aq.AQI <= 200:
		aq.Level = AirQualityUnhealthy
		aq.Description = "Some members of the general public may experience health effects."
	case aq.AQI <= 300:
		aq.Level = AirQualityVeryUnhealthy
		aq.Description = "Health alert: the risk of health effects is increased for everyone."
	default:
		aq.Level = AirQualityHazardous
		aq.Description = "Health warning of emergency conditions: everyone is more likely to be affected."
	}

	aq.PM25 = int(float64(aq.AQI) * 0.5 * (1 + r.rng.Float64()*0.4))
	aq.PM10 = int(float64(aq.PM25) * 1.5 * (1 + r.rng.Float64()*0.3))
	aq.Ozone = 20 + r.rng.IntN(80)
	return aq
}

func (r *reportGenerator) uvIndex(condition Condition, cloudCover int) UVIndex {
	absLat := math.Abs(r.coords.Latitude)
	latitudeFactor := 1.0 - absLat/90.0
	var seasonFactor float64
	switch {
	case r.month >= 5 && r.month <= 8:
		seasonFactor = 1.2
	case r.month >= 11 || r.month <= 2:
		seasonFactor = 0.6
	default:
		seasonFactor = 0.9
	}

	value := int(11.0 * latitudeFactor * seasonFactor)
	value -= int(float64(cloudCover) * 0.08)
	switch condition {
	case ConditionSunny, ConditionClear:
		value += 1 + r.rng.IntN(2)
	case ConditionCloudy, ConditionOvercast:
		value -= 2 + r.rng.IntN(2)
	case ConditionRainy, ConditionStormy:
		value -= 4 + r.rng.IntN(3)
	}
	value = clamp(value, 0, 11)

	uv := UVIndex{Value: value}
	switch {
	case value <= 2:
		uv.Level = UVLow
		uv.Description = "No protection required. You can safely stay outside."
	case value <= 5:
		uv.Level = UVModerate
		uv.Description = "Seek shade during midday hours. Wear sunscreen and a hat."
	case value <= 7:
		uv.Level = UVHigh
		uv.Description = "Protection essential. Seek shade during midday hours."
	case value <= 10:
		uv.Level = UVVeryHigh
		uv.Description = "Extra protection needed. Avoid sun exposure during midday."
	default:
		uv.Level = UVExtreme
		uv.Description = "Take all precautions. Unprotected skin will burn quickly."
	}
	return uv
}

// astronomy uses simplified declination math for sunrise/sunset,
// and a 29.5-day cycle for the moon phase.
func (r *reportGenerator) astronomy() Astronomy {
	dayOfYear := r.target.YearDay()

	declination := 23.45 * math.Sin(2*math.Pi*float64(dayOfYear-81)/365)
	latRad := r.coords.Latitude * math.Pi / 180
	declRad := declination * math.Pi / 180
	cosH := -math.Tan(latRad) * math.Tan(declRad)
	cosH = math.Max(-1, math.Min(1, cosH)) // polar day/night clamp
	hourAngle := math.Acos(cosH)
	daylight := 2 * hourAngle * 12 / math.Pi

	sunrise := formatHM(12 - daylight/2)
	sunset := formatHM(12 + daylight/2)

	phaseIndex := (dayOfYear * 8 / 30) % 8
	moonIllum := int(math.Abs(math.Sin(float64(dayOfYear)*2*math.Pi/29.5)) * 100)

	moonriseOffset := r.rng.IntN(120) - 60
	moonsetOffset := r.rng.IntN(120) - 60
	moonrise := formatHM(12 - daylight/2 + float64(moonriseOffset)/60.0)
	moonset := formatHM(12 + daylight/2 + float64(moonsetOffset)/60.0)

	return Astronomy{
		Sunrise:          sunrise,
		Sunset:           sunset,
		Moonrise:         moonrise,
		Moonset:          moonset,
		MoonPhase:        moonPhases[phaseIndex],
		MoonIllumination: moonIllum,
	}
}

// parseTargetDate accepts an empty string (= today UTC) or
// "YYYY-MM-DD" and returns a time.Time pinned to 00:00 UTC of the
// target day.
func parseTargetDate(s string) (time.Time, error) {
	if s == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("fakeweather.parseDate: invalid date %q (want YYYY-MM-DD): %w", s, err)
	}
	return t, nil
}

// newRng builds a deterministic PRNG seeded from location + date.
func newRng(location string, date time.Time) *rand.Rand {
	seed := uint64(date.Unix())
	for _, c := range location {
		seed = seed*31 + uint64(c)
	}
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

// monthForLookup decides which month to feed into the climate-profile
// table. For a known city in the southern hemisphere, months are flipped
// (so July reads as January temps). For unknown locations, the lookup uses the
// month as-is — the previous algorithm pseudo-randomly assigned
// southern hemispheres to unknown cities, which produced "summer is
// freezing" reports.
func monthForLookup(date time.Time, latitude float64, knownCity bool) int {
	month := int(date.Month())
	if knownCity && latitude < 0 {
		month = ((month + 5) % 12) + 1 // 1..12 → flip 6 months
	}
	return month
}

// coordinatesFor returns the location's coordinates plus a flag for
// whether the lookup was a known-city hit. Unknown locations get
// derived (deterministic, northern-hemisphere) coords so the season
// math doesn't randomly flip; see [knownCities] for the gazetteer.
func coordinatesFor(location string, rng *rand.Rand) (Coordinates, bool) {
	if profile, ok := lookupCity(location); ok {
		return Coordinates{
			Latitude:  profile.Latitude,
			Longitude: profile.Longitude,
			Elevation: profile.Elevation,
		}, true
	}

	// Derive deterministic latitude in [10, 60] (mid-northern latitudes)
	// and longitude in [-180, 180] from the location string.
	var latSeed, lonSeed float64
	for _, c := range location {
		latSeed += float64(c)
		lonSeed += float64(c) * 1.5
	}
	lat := 10 + math.Mod(latSeed, 50)
	lon := math.Mod(lonSeed, 360) - 180
	elevation := rng.IntN(500)
	return Coordinates{
		Latitude:  math.Round(lat*10000) / 10000,
		Longitude: math.Round(lon*10000) / 10000,
		Elevation: elevation,
	}, false
}
