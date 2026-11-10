package fakeweather

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

const (
	fullPercentage         = 100
	minimumBaselineWindKPH = 5.0
	maximumBaselineWindKPH = 20.0
	syntheticReportSource  = "fakeweather (synthesized; not real weather data)"
)

type reportGenerator struct {
	location string
	target   time.Time
	rng      *rand.Rand
	zone     climateZone
	coords   Coordinates
	profile  climateProfile
	month    int
}

func generate(req *Request) (*Response, error) {
	generator, err := newReportGenerator(req)
	if err != nil {
		return nil, err
	}
	report := generator.report()
	// Detail selection cannot change random draws that establish the weather.
	if !req.IncludeAirQuality {
		report.AirQuality = nil
	}
	if !req.IncludeHourly {
		report.HourlyForecast = nil
	}
	return report, nil
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
		location: req.Location,
		target:   target,
		rng:      rng,
		zone:     zone,
		coords:   coords,
		profile:  climateProfiles[zone],
		month:    monthForLookup(target, coords.Latitude, knownCity),
	}, nil
}

func (r *reportGenerator) report() *Response {
	const dailyTemperatureJitterCelsius = 2

	// Date-only reports use the daily mean so midnight does not bias the result.
	mean := r.profile.mean[r.month-1]

	// Elevation correction: approximately 0.6 degrees Celsius per 100 meters.
	elevDrop := int(float64(r.coords.Elevation) * 0.006)
	mean -= elevDrop

	jitter := r.rng.IntN(2*dailyTemperatureJitterCelsius+1) - dailyTemperatureJitterCelsius
	current := clamp(mean+jitter, r.profile.floor, r.profile.ceiling)

	candidates := r.zone.candidateConditions(current, r.month)
	condition := candidates[r.rng.IntN(len(candidates))]

	wind := r.wind(condition)
	humidity := r.humidity(condition)
	pressure := r.pressure(condition)
	visibility := r.visibility(condition, humidity)
	cloudCover := r.cloudCover(condition)

	airQuality := r.airQuality(condition)
	uvIndex := r.uvIndex(condition, cloudCover)
	astronomy := r.astronomy()
	hourlyForecast := r.hourlyForecast(current, condition)
	temperature := newTemperature(hourlyForecast, humidity, wind.Speed)
	precipitation := newPrecipitation(hourlyForecast, r.precipitationType(temperature.Value), r.precipitationProbability(condition))
	dewPoint := calculateDewPoint(temperature.Value, humidity)
	alerts := r.alerts(condition, temperature.Value, wind.Speed)
	description := buildDescription(condition, temperature.Value, wind, humidity, precipitation)

	startOfDay := time.Date(r.target.Year(), r.target.Month(), r.target.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(hoursPerDay * time.Hour)

	return &Response{
		Location:    r.location,
		Coordinates: r.coords,
		Timestamp: TimeRange{
			Start: startOfDay.Unix(),
			End:   endOfDay.Unix(),
		},
		Temperature:    temperature,
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
		HourlyForecast: hourlyForecast[:],
		Alerts:         alerts,
		Source:         syntheticReportSource,
		LastUpdated:    startOfDay.Unix(),
	}
}

func (r *reportGenerator) hourlyForecast(dailyMean int, condition Condition) [hoursPerDay]HourlyForecast {
	const (
		temperatureJitterCelsius     = 1
		conditionChangeProbability   = 0.2
		maximumPrecipitationMM       = 5.0
		minimumHumidityPercent       = 50
		maximumHumidityPercent       = 80
		nightStartHour               = 22
		nightEndHour                 = 6
		nightHumidityIncreasePercent = 10
	)

	var out [hoursPerDay]HourlyForecast
	for i := range out {
		hour := time.Date(r.target.Year(), r.target.Month(), r.target.Day(), i, 0, 0, 0, time.UTC)

		variation := r.profile.dailyVariation(i)
		hourTemp := clamp(dailyMean+variation+r.rng.IntN(2*temperatureJitterCelsius+1)-temperatureJitterCelsius, r.profile.floor, r.profile.ceiling)

		hourCondition := condition
		if r.rng.Float64() < conditionChangeProbability {
			alt := r.zone.candidateConditions(hourTemp, r.month)
			hourCondition = alt[r.rng.IntN(len(alt))]
		}

		precip := 0.0
		if hourCondition.hasPrecipitation() {
			precip = roundMeasurement(r.rng.Float64() * maximumPrecipitationMM)
		}

		humidity := minimumHumidityPercent + r.rng.IntN(maximumHumidityPercent-minimumHumidityPercent)
		if i >= nightStartHour || i <= nightEndHour {
			humidity = min(humidity+nightHumidityIncreasePercent, fullPercentage)
		}

		out[i] = HourlyForecast{
			Time:          hour.Unix(),
			Temperature:   hourTemp,
			Condition:     hourCondition,
			Precipitation: precip,
			Humidity:      humidity,
			WindSpeed:     roundMeasurement(minimumBaselineWindKPH + r.rng.Float64()*(maximumBaselineWindKPH-minimumBaselineWindKPH)),
		}
	}
	return out
}

func (r *reportGenerator) alerts(condition Condition, temp int, windSpeed float64) []Alert {
	const (
		moderateHeatCelsius     = 35
		severeHeatCelsius       = 40
		moderateColdCelsius     = -10
		severeColdCelsius       = -20
		moderateWindKPH         = 50
		severeWindKPH           = 70
		windAlertDuration       = 12 * time.Hour
		stormAlertDuration      = 6 * time.Hour
		blizzardAlertDuration   = 12 * time.Hour
		typhoonAlertDuration    = 48 * time.Hour
		typhoonAlertProbability = 0.05
	)

	var alerts []Alert
	day := r.target.Add(hoursPerDay * time.Hour)

	if temp >= moderateHeatCelsius {
		severity := AlertSeverityModerate
		if temp >= severeHeatCelsius {
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
	if temp <= moderateColdCelsius {
		severity := AlertSeverityModerate
		if temp <= severeColdCelsius {
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
	if windSpeed >= moderateWindKPH {
		severity := AlertSeverityModerate
		if windSpeed >= severeWindKPH {
			severity = AlertSeveritySevere
		}
		alerts = append(alerts, Alert{
			Type:        AlertWind,
			Severity:    severity,
			Title:       "High Wind Warning",
			Description: fmt.Sprintf("Wind speeds may reach %.*f %s. Secure loose objects and avoid outdoor activities.", measurementDecimalPlaces, windSpeed, windSpeedUnit),
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(windAlertDuration).Unix(),
		})
	}
	if condition == ConditionStormy {
		alerts = append(alerts, Alert{
			Type:        AlertStorm,
			Severity:    AlertSeveritySevere,
			Title:       "Severe Storm Warning",
			Description: "Severe thunderstorms expected. Stay indoors and avoid travel if possible.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(stormAlertDuration).Unix(),
		})
	}
	if condition == ConditionBlizzard {
		alerts = append(alerts, Alert{
			Type:        AlertSnow,
			Severity:    AlertSeveritySevere,
			Title:       "Blizzard Warning",
			Description: "Blizzard conditions expected with heavy snow and strong winds. Travel is strongly discouraged.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(blizzardAlertDuration).Unix(),
		})
	}
	if r.zone.typhoonSeason(r.month) && r.rng.Float64() < typhoonAlertProbability {
		alerts = append(alerts, Alert{
			Type:        AlertTyphoon,
			Severity:    AlertSeverityExtreme,
			Title:       "Typhoon Warning",
			Description: "A typhoon is approaching. Evacuate if instructed by authorities and prepare for extreme weather.",
			StartTime:   r.target.Unix(),
			EndTime:     r.target.Add(typhoonAlertDuration).Unix(),
		})
	}
	return alerts
}

func (r *reportGenerator) wind(condition Condition) Wind {
	const (
		severeWeatherMaximumIncreaseKPH = 30.0
		wetWeatherMaximumIncreaseKPH    = 15.0
		clearWeatherSpeedFactor         = 0.6
		desertMaximumIncreaseKPH        = 10.0
		oceanicMaximumIncreaseKPH       = 8.0
		alpineMaximumIncreaseKPH        = 12.0
		minimumGustFactor               = 1.2
		maximumGustFactor               = 1.5
	)

	speed := minimumBaselineWindKPH + r.rng.Float64()*(maximumBaselineWindKPH-minimumBaselineWindKPH)
	switch condition {
	case ConditionStormy, ConditionBlizzard:
		speed += r.rng.Float64() * severeWeatherMaximumIncreaseKPH
	case ConditionRainy, ConditionSnowy:
		speed += r.rng.Float64() * wetWeatherMaximumIncreaseKPH
	case ConditionSunny, ConditionClear:
		speed *= clearWeatherSpeedFactor
	}
	switch r.zone {
	case zoneDesert:
		speed += r.rng.Float64() * desertMaximumIncreaseKPH
	case zoneOceanic:
		speed += r.rng.Float64() * oceanicMaximumIncreaseKPH
	case zoneAlpine:
		speed += r.rng.Float64() * alpineMaximumIncreaseKPH
	}
	speed += float64(r.coords.Elevation) * 0.01

	degree := r.rng.IntN(degreesPerTurn)
	gust := speed * (minimumGustFactor + r.rng.Float64()*(maximumGustFactor-minimumGustFactor))
	return Wind{
		Speed:     roundMeasurement(speed),
		Unit:      windSpeedUnit,
		Direction: directionFromDegree(degree),
		Degree:    degree,
		Gust:      roundMeasurement(gust),
	}
}

func (r *reportGenerator) humidity(condition Condition) int {
	const (
		wetMinimumIncrease      = 20
		wetMaximumIncrease      = 40
		snowMinimumIncrease     = 15
		snowMaximumIncrease     = 30
		snowMaximumHumidity     = 95
		cloudyMaximumIncrease   = 15
		clearMinimumAdjustment  = -20
		clearMaximumAdjustment  = 0
		clearMinimumHumidity    = 10
		dustyMinimumAdjustment  = -30
		dustyMaximumAdjustment  = -15
		dustyMinimumHumidity    = 5
		defaultAdjustmentRadius = 10
	)

	base := r.zone.baseHumidity(r.month)
	switch condition {
	case ConditionRainy, ConditionStormy, ConditionFoggy, ConditionHumid, ConditionDrizzle:
		return min(base+wetMinimumIncrease+r.rng.IntN(wetMaximumIncrease-wetMinimumIncrease), fullPercentage)
	case ConditionSnowy, ConditionBlizzard:
		return min(base+snowMinimumIncrease+r.rng.IntN(snowMaximumIncrease-snowMinimumIncrease), snowMaximumHumidity)
	case ConditionCloudy, ConditionPartlyCloudy, ConditionOvercast:
		return base + r.rng.IntN(cloudyMaximumIncrease)
	case ConditionSunny, ConditionClear, ConditionHot:
		return max(base+clearMinimumAdjustment+r.rng.IntN(clearMaximumAdjustment-clearMinimumAdjustment), clearMinimumHumidity)
	case ConditionDusty, ConditionHazy:
		return max(base+dustyMinimumAdjustment+r.rng.IntN(dustyMaximumAdjustment-dustyMinimumAdjustment), dustyMinimumHumidity)
	}
	return base + r.rng.IntN(2*defaultAdjustmentRadius) - defaultAdjustmentRadius
}

func (r *reportGenerator) pressure(condition Condition) int {
	const (
		seaLevelPressureHPa       = 1013
		wetMinimumDropHPa         = 10
		wetMaximumDropHPa         = 25
		clearMinimumIncreaseHPa   = 5
		clearMaximumIncreaseHPa   = 15
		cloudyAdjustmentRadiusHPa = 5
	)

	base := seaLevelPressureHPa - r.coords.Elevation/8
	switch condition {
	case ConditionStormy, ConditionRainy:
		base += -wetMinimumDropHPa - r.rng.IntN(wetMaximumDropHPa-wetMinimumDropHPa)
	case ConditionSunny, ConditionClear:
		base += clearMinimumIncreaseHPa + r.rng.IntN(clearMaximumIncreaseHPa-clearMinimumIncreaseHPa)
	case ConditionCloudy, ConditionPartlyCloudy:
		base += r.rng.IntN(2*cloudyAdjustmentRadiusHPa) - cloudyAdjustmentRadiusHPa
	}
	return base
}

func (r *reportGenerator) visibility(condition Condition, humidity int) int {
	const (
		minimumVisibilityKM          = 1
		fogMaximumKM                 = 3
		wetMinimumKM                 = 3
		wetMaximumKM                 = 8
		severeMaximumKM              = 4
		dustyMinimumKM               = 2
		dustyMaximumKM               = 8
		cloudyMinimumKM              = 8
		cloudyMaximumKM              = 15
		clearMinimumKM               = 15
		clearMaximumKM               = 50
		defaultMinimumKM             = 10
		defaultMaximumKM             = 20
		humidityAttenuationThreshold = 85
		humidityAttenuationFactor    = 0.7
	)

	var base int
	switch condition {
	case ConditionFoggy:
		base = r.rng.IntN(fogMaximumKM-minimumVisibilityKM) + minimumVisibilityKM
	case ConditionRainy, ConditionSnowy:
		base = wetMinimumKM + r.rng.IntN(wetMaximumKM-wetMinimumKM)
	case ConditionStormy, ConditionBlizzard:
		base = minimumVisibilityKM + r.rng.IntN(severeMaximumKM-minimumVisibilityKM)
	case ConditionDusty, ConditionHazy:
		base = dustyMinimumKM + r.rng.IntN(dustyMaximumKM-dustyMinimumKM)
	case ConditionCloudy:
		base = cloudyMinimumKM + r.rng.IntN(cloudyMaximumKM-cloudyMinimumKM)
	case ConditionSunny, ConditionClear:
		base = clearMinimumKM + r.rng.IntN(clearMaximumKM-clearMinimumKM)
	default:
		base = defaultMinimumKM + r.rng.IntN(defaultMaximumKM-defaultMinimumKM)
	}
	if humidity > humidityAttenuationThreshold {
		base = int(float64(base) * humidityAttenuationFactor)
	}
	return max(minimumVisibilityKM, base)
}

func (r *reportGenerator) cloudCover(condition Condition) int {
	const (
		clearMaximumPercent        = 15
		partlyCloudyMinimumPercent = 25
		partlyCloudyMaximumPercent = 60
		cloudyMinimumPercent       = 75
		wetMinimumPercent          = 90
		defaultMinimumPercent      = 40
		defaultMaximumPercent      = 80
	)

	switch condition {
	case ConditionSunny, ConditionClear:
		return r.rng.IntN(clearMaximumPercent)
	case ConditionPartlyCloudy:
		return partlyCloudyMinimumPercent + r.rng.IntN(partlyCloudyMaximumPercent-partlyCloudyMinimumPercent)
	case ConditionCloudy, ConditionOvercast:
		return cloudyMinimumPercent + r.rng.IntN(fullPercentage-cloudyMinimumPercent)
	case ConditionRainy, ConditionSnowy, ConditionStormy:
		return wetMinimumPercent + r.rng.IntN(fullPercentage-wetMinimumPercent)
	case ConditionFoggy:
		return fullPercentage
	}
	return defaultMinimumPercent + r.rng.IntN(defaultMaximumPercent-defaultMinimumPercent)
}

func (r *reportGenerator) precipitationType(temp int) PrecipitationType {
	const (
		mixedPrecipitationMaximumCelsius = 3
		sleetProbability                 = 0.3
	)

	switch {
	case temp < freezingPointCelsius:
		return PrecipitationSnow
	case temp < mixedPrecipitationMaximumCelsius:
		if r.rng.Float64() < sleetProbability {
			return PrecipitationSleet
		}
		return PrecipitationSnow
	}
	return PrecipitationRain
}

func (r *reportGenerator) precipitationProbability(condition Condition) int {
	const (
		severeMinimumPercent   = 85
		wetMinimumPercent      = 60
		wetMaximumPercent      = 90
		drizzleMinimumPercent  = 40
		drizzleMaximumPercent  = 70
		defaultMinimumPercent  = 30
		defaultMaximumPercent  = 70
		monsoonIncreasePercent = 15
	)

	var probability int
	switch condition {
	case ConditionStormy, ConditionBlizzard:
		probability = severeMinimumPercent + r.rng.IntN(fullPercentage-severeMinimumPercent)
	case ConditionRainy, ConditionSnowy:
		probability = wetMinimumPercent + r.rng.IntN(wetMaximumPercent-wetMinimumPercent)
	case ConditionDrizzle:
		probability = drizzleMinimumPercent + r.rng.IntN(drizzleMaximumPercent-drizzleMinimumPercent)
	default:
		probability = defaultMinimumPercent + r.rng.IntN(defaultMaximumPercent-defaultMinimumPercent)
	}
	if r.zone.monsoon().covers(r.month) {
		probability = min(fullPercentage, probability+monsoonIncreasePercent)
	}
	return probability
}

func (r *reportGenerator) airQuality(condition Condition) *AirQuality {
	const (
		baselineAQI             = 50
		pollutedMinimumAQI      = 80
		pollutedMaximumAQI      = 120
		fogMinimumIncrease      = 40
		fogMaximumIncrease      = 70
		rainMinimumReduction    = 20
		rainMaximumReduction    = 40
		windMinimumReduction    = 10
		windMaximumReduction    = 25
		desertMinimumIncrease   = 10
		desertMaximumIncrease   = 30
		maximumAQI              = 500
		goodMaximumAQI          = 50
		moderateMaximumAQI      = 100
		sensitiveMaximumAQI     = 150
		unhealthyMaximumAQI     = 200
		veryUnhealthyMaximumAQI = 300
		ozoneMinimum            = 20
		ozoneMaximum            = 100
		pm25RelativeJitter      = 0.4
		pm10RelativeJitter      = 0.3
	)

	aq := &AirQuality{}
	aqi := baselineAQI

	if profile, ok := knownCities.lookup(r.location); ok && profile.Polluted {
		aqi = pollutedMinimumAQI + r.rng.IntN(pollutedMaximumAQI-pollutedMinimumAQI)
	}

	switch condition {
	case ConditionFoggy, ConditionHazy:
		aqi += fogMinimumIncrease + r.rng.IntN(fogMaximumIncrease-fogMinimumIncrease)
	case ConditionRainy, ConditionStormy:
		aqi -= rainMinimumReduction + r.rng.IntN(rainMaximumReduction-rainMinimumReduction)
	case ConditionWindy:
		aqi -= windMinimumReduction + r.rng.IntN(windMaximumReduction-windMinimumReduction)
	}
	if r.zone == zoneDesert {
		aqi += desertMinimumIncrease + r.rng.IntN(desertMaximumIncrease-desertMinimumIncrease)
	}

	aq.AQI = clamp(aqi, 0, maximumAQI)
	switch {
	case aq.AQI <= goodMaximumAQI:
		aq.Level = AirQualityGood
		aq.Description = "Air quality is satisfactory, and air pollution poses little or no risk."
	case aq.AQI <= moderateMaximumAQI:
		aq.Level = AirQualityModerate
		aq.Description = "Air quality is acceptable. There may be a risk for some people sensitive to air pollution."
	case aq.AQI <= sensitiveMaximumAQI:
		aq.Level = AirQualityUnhealthyForSensitiveGroups
		aq.Description = "Members of sensitive groups may experience health effects."
	case aq.AQI <= unhealthyMaximumAQI:
		aq.Level = AirQualityUnhealthy
		aq.Description = "Some members of the general public may experience health effects."
	case aq.AQI <= veryUnhealthyMaximumAQI:
		aq.Level = AirQualityVeryUnhealthy
		aq.Description = "Health alert: the risk of health effects is increased for everyone."
	default:
		aq.Level = AirQualityHazardous
		aq.Description = "Health warning of emergency conditions: everyone is more likely to be affected."
	}

	aq.PM25 = int(float64(aq.AQI) * 0.5 * (1 + r.rng.Float64()*pm25RelativeJitter))
	aq.PM10 = int(float64(aq.PM25) * 1.5 * (1 + r.rng.Float64()*pm10RelativeJitter))
	aq.Ozone = ozoneMinimum + r.rng.IntN(ozoneMaximum-ozoneMinimum)
	return aq
}

func (r *reportGenerator) uvIndex(condition Condition, cloudCover int) UVIndex {
	const (
		summerFirstMonth       = int(time.May)
		summerLastMonth        = int(time.August)
		winterFirstMonth       = int(time.November)
		winterLastMonth        = int(time.February)
		summerFactor           = 1.2
		winterFactor           = 0.6
		shoulderSeasonFactor   = 0.9
		equatorialIndex        = 11.0
		maximumGeneratedIndex  = 11
		clearMinimumIncrease   = 1
		clearMaximumIncrease   = 3
		cloudyMinimumReduction = 2
		cloudyMaximumReduction = 4
		wetMinimumReduction    = 4
		wetMaximumReduction    = 7
		lowMaximumIndex        = 2
		moderateMaximumIndex   = 5
		highMaximumIndex       = 7
		veryHighMaximumIndex   = 10
	)

	absLat := math.Abs(r.coords.Latitude)
	latitudeFactor := 1.0 - absLat/90.0
	var seasonFactor float64
	switch {
	case r.month >= summerFirstMonth && r.month <= summerLastMonth:
		seasonFactor = summerFactor
	case r.month >= winterFirstMonth || r.month <= winterLastMonth:
		seasonFactor = winterFactor
	default:
		seasonFactor = shoulderSeasonFactor
	}

	value := int(equatorialIndex * latitudeFactor * seasonFactor)
	value -= int(float64(cloudCover) * 0.08)
	switch condition {
	case ConditionSunny, ConditionClear:
		value += clearMinimumIncrease + r.rng.IntN(clearMaximumIncrease-clearMinimumIncrease)
	case ConditionCloudy, ConditionOvercast:
		value -= cloudyMinimumReduction + r.rng.IntN(cloudyMaximumReduction-cloudyMinimumReduction)
	case ConditionRainy, ConditionStormy:
		value -= wetMinimumReduction + r.rng.IntN(wetMaximumReduction-wetMinimumReduction)
	}
	value = clamp(value, 0, maximumGeneratedIndex)

	uv := UVIndex{Value: value}
	switch {
	case value <= lowMaximumIndex:
		uv.Level = UVLow
		uv.Description = "No protection required. You can safely stay outside."
	case value <= moderateMaximumIndex:
		uv.Level = UVModerate
		uv.Description = "Seek shade during midday hours. Wear sunscreen and a hat."
	case value <= highMaximumIndex:
		uv.Level = UVHigh
		uv.Description = "Protection essential. Seek shade during midday hours."
	case value <= veryHighMaximumIndex:
		uv.Level = UVVeryHigh
		uv.Description = "Extra protection needed. Avoid sun exposure during midday."
	default:
		uv.Level = UVExtreme
		uv.Description = "Take all precautions. Unprotected skin will burn quickly."
	}
	return uv
}

// Sun times use approximate solar declination.
func (r *reportGenerator) astronomy() Astronomy {
	const (
		solarNoonHour          = hoursPerDay / 2
		moonEventJitterMinutes = 60
	)

	dayOfYear := r.target.YearDay()

	declination := 23.45 * math.Sin(2*math.Pi*float64(dayOfYear-81)/365)
	latRad := r.coords.Latitude * math.Pi / 180
	declRad := declination * math.Pi / 180
	cosH := -math.Tan(latRad) * math.Tan(declRad)
	cosH = math.Max(-1, math.Min(1, cosH)) // polar day/night clamp
	hourAngle := math.Acos(cosH)
	daylight := 2 * hourAngle * solarNoonHour / math.Pi

	sunrise := formatHM(solarNoonHour - daylight/2)
	sunset := formatHM(solarNoonHour + daylight/2)

	phase := newLunarPhase(r.target)

	moonriseOffset := r.rng.IntN(2*moonEventJitterMinutes) - moonEventJitterMinutes
	moonsetOffset := r.rng.IntN(2*moonEventJitterMinutes) - moonEventJitterMinutes
	moonrise := formatHM(solarNoonHour - daylight/2 + float64(moonriseOffset)/float64(minutesPerHour))
	moonset := formatHM(solarNoonHour + daylight/2 + float64(moonsetOffset)/float64(minutesPerHour))

	return Astronomy{
		Sunrise:          sunrise,
		Sunset:           sunset,
		Moonrise:         moonrise,
		Moonset:          moonset,
		MoonPhase:        phase.name(),
		MoonIllumination: phase.illumination(),
	}
}

func parseTargetDate(s string) (time.Time, error) {
	if s == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("fakeweather: invalid date %q, want YYYY-MM-DD: %w", s, err)
	}
	return t, nil
}

func newRng(location string, date time.Time) *rand.Rand {
	seed := uint64(date.Unix())
	for _, c := range location {
		seed = seed*31 + uint64(c)
	}
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

func monthForLookup(date time.Time, latitude float64, knownCity bool) int {
	month := int(date.Month())
	if knownCity && latitude < 0 {
		month = ((month + 5) % 12) + 1
	}
	return month
}

// Unknown locations use northern latitudes to keep the input calendar season.
func coordinatesFor(location string, rng *rand.Rand) (Coordinates, bool) {
	const (
		minimumGeneratedLatitude        = 10
		maximumGeneratedLatitude        = 60
		maximumGeneratedElevationMeters = 500
		coordinateDecimalScale          = 10000
	)
	if profile, ok := knownCities.lookup(location); ok {
		return Coordinates{
			Latitude:  profile.Latitude,
			Longitude: profile.Longitude,
			Elevation: profile.Elevation,
		}, true
	}

	var latSeed, lonSeed float64
	for _, c := range location {
		latSeed += float64(c)
		lonSeed += float64(c) * 1.5
	}
	lat := minimumGeneratedLatitude + math.Mod(latSeed, maximumGeneratedLatitude-minimumGeneratedLatitude)
	lon := math.Mod(lonSeed, degreesPerTurn) - degreesPerTurn/2
	elevation := rng.IntN(maximumGeneratedElevationMeters)
	return Coordinates{
		Latitude:  math.Round(lat*coordinateDecimalScale) / coordinateDecimalScale,
		Longitude: math.Round(lon*coordinateDecimalScale) / coordinateDecimalScale,
		Elevation: elevation,
	}, false
}
