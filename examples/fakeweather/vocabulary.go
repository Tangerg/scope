package fakeweather

import "fmt"

type Condition string

const (
	ConditionBlizzard     Condition = "Blizzard"
	ConditionClear        Condition = "Clear"
	ConditionCloudy       Condition = "Cloudy"
	ConditionCold         Condition = "Cold"
	ConditionDrizzle      Condition = "Drizzle"
	ConditionDusty        Condition = "Dusty"
	ConditionFoggy        Condition = "Foggy"
	ConditionFreezing     Condition = "Freezing"
	ConditionHazy         Condition = "Hazy"
	ConditionHot          Condition = "Hot"
	ConditionHumid        Condition = "Humid"
	ConditionMild         Condition = "Mild"
	ConditionOvercast     Condition = "Overcast"
	ConditionPartlyCloudy Condition = "Partly Cloudy"
	ConditionRainy        Condition = "Rainy"
	ConditionSnowy        Condition = "Snowy"
	ConditionStormy       Condition = "Stormy"
	ConditionSunny        Condition = "Sunny"
	ConditionWindy        Condition = "Windy"
)

func (c Condition) hasPrecipitation() bool {
	switch c {
	case ConditionRainy, ConditionSnowy, ConditionStormy, ConditionBlizzard, ConditionDrizzle:
		return true
	}
	return false
}

func (c Condition) summary(precip *Precipitation) string {
	switch c {
	case ConditionSunny:
		return "Clear skies with abundant sunshine throughout the day."
	case ConditionPartlyCloudy:
		return "Mix of sun and clouds with pleasant weather conditions."
	case ConditionCloudy:
		return "Overcast skies with cloud cover throughout the day."
	case ConditionRainy:
		return "Rainy conditions expected." + precip.amountSentence("Rainfall")
	case ConditionStormy:
		return "Severe thunderstorms with heavy rain and strong winds. Lightning activity expected."
	case ConditionSnowy:
		return "Snow is expected." + precip.amountSentence("Snowfall")
	case ConditionBlizzard:
		return "Blizzard conditions with heavy snow and very strong winds. Visibility severely reduced."
	case ConditionFoggy:
		return "Dense fog reducing visibility significantly. Drive with caution."
	case ConditionHot:
		return "Hot and sunny conditions. Take precautions against heat."
	}
	return fmt.Sprintf("%s weather conditions expected.", c)
}

type PrecipitationType string

const (
	PrecipitationRain  PrecipitationType = "rain"
	PrecipitationSleet PrecipitationType = "sleet"
	PrecipitationSnow  PrecipitationType = "snow"
)

type PrecipitationIntensity string

const (
	PrecipitationLight    PrecipitationIntensity = "light"
	PrecipitationModerate PrecipitationIntensity = "moderate"
	PrecipitationHeavy    PrecipitationIntensity = "heavy"
)

type AlertSeverity string

const (
	AlertSeverityModerate AlertSeverity = "moderate"
	AlertSeveritySevere   AlertSeverity = "severe"
	AlertSeverityExtreme  AlertSeverity = "extreme"
)

type AlertType string

const (
	AlertCold    AlertType = "cold"
	AlertHeat    AlertType = "heat"
	AlertSnow    AlertType = "snow"
	AlertStorm   AlertType = "storm"
	AlertTyphoon AlertType = "typhoon"
	AlertWind    AlertType = "wind"
)

// AirQualityLevel is the US AQI qualitative scale.
type AirQualityLevel string

const (
	AirQualityGood                        AirQualityLevel = "Good"
	AirQualityModerate                    AirQualityLevel = "Moderate"
	AirQualityUnhealthyForSensitiveGroups AirQualityLevel = "Unhealthy for Sensitive Groups"
	AirQualityUnhealthy                   AirQualityLevel = "Unhealthy"
	AirQualityVeryUnhealthy               AirQualityLevel = "Very Unhealthy"
	AirQualityHazardous                   AirQualityLevel = "Hazardous"
)

// UVLevel is the WHO UV-index qualitative scale.
type UVLevel string

const (
	UVLow      UVLevel = "Low"
	UVModerate UVLevel = "Moderate"
	UVHigh     UVLevel = "High"
	UVVeryHigh UVLevel = "Very High"
	UVExtreme  UVLevel = "Extreme"
)
