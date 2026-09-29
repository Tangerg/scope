package fakeweather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type Request struct {
	Location string `json:"location" jsonschema:"minLength=1" jsonschema_description:"Geographic location, such as a city, city and country, or street address. English and local-language names are accepted."`

	Date string `json:"date,omitempty" jsonschema:"pattern=^\\d{4}-\\d{2}-\\d{2}$" jsonschema_description:"Forecast date in YYYY-MM-DD format. Omit to use the current UTC date."`

	IncludeHourly bool `json:"include_hourly,omitzero" jsonschema_description:"Include a 24-hour forecast. Defaults to false."`

	IncludeAirQuality bool `json:"include_air_quality,omitzero" jsonschema_description:"Include AQI and pollutant concentrations. Defaults to false."`
}

type Response struct {
	Location       string           `json:"location"`
	Coordinates    Coordinates      `json:"coordinates"`
	Timestamp      TimeRange        `json:"timestamp"`
	Temperature    Temperature      `json:"temperature"`
	Condition      Condition        `json:"condition"`
	Description    string           `json:"description"`
	Humidity       int              `json:"humidity"`
	Pressure       int              `json:"pressure"`    // hPa
	Visibility     int              `json:"visibility"`  // km
	CloudCover     int              `json:"cloud_cover"` // 0-100
	DewPoint       int              `json:"dew_point"`
	Wind           Wind             `json:"wind"`
	Precipitation  *Precipitation   `json:"precipitation,omitzero"`
	AirQuality     *AirQuality      `json:"air_quality,omitzero"`
	UVIndex        UVIndex          `json:"uv_index"`
	Astronomy      Astronomy        `json:"astronomy"`
	HourlyForecast []HourlyForecast `json:"hourly_forecast,omitempty"`
	Alerts         []Alert          `json:"alerts,omitempty"`
	Source         string           `json:"source"`
	LastUpdated    int64            `json:"last_updated"` // Unix seconds at the start of the target date
}

type Coordinates struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Elevation int     `json:"elevation"` // meters
}

type Temperature struct {
	Value     int    `json:"value"` // daily mean, not an instantaneous reading
	Unit      string `json:"unit"`  // always "Celsius"
	FeelsLike int    `json:"feels_like"`
	Min       int    `json:"min"`
	Max       int    `json:"max"`
}

type Wind struct {
	Speed     float64 `json:"speed"`
	Unit      string  `json:"unit"` // always "km/h"
	Direction string  `json:"direction"`
	Degree    int     `json:"degree"`
	Gust      float64 `json:"gust"`
}

type Precipitation struct {
	Type        PrecipitationType      `json:"type"`
	Probability int                    `json:"probability"` // 0-100
	Amount      float64                `json:"amount"`      // mm
	Intensity   PrecipitationIntensity `json:"intensity"`
}

// A nil Precipitation describes a dry day and contributes no sentence.
func (p *Precipitation) amountSentence(kind string) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" %s amount: %.1f mm. %s intensity.", kind, p.Amount, p.Intensity)
}

type AirQuality struct {
	AQI         int             `json:"aqi"`
	Level       AirQualityLevel `json:"level"`
	PM25        int             `json:"pm2_5"`
	PM10        int             `json:"pm10"`
	Ozone       int             `json:"ozone"`
	Description string          `json:"description"`
}

// UVIndex per WHO levels (0-11+).
type UVIndex struct {
	Value       int     `json:"value"`
	Level       UVLevel `json:"level"`
	Description string  `json:"description"`
}

// Sun and moon times are approximate solar-local HH:MM values.
type Astronomy struct {
	Sunrise          string `json:"sunrise"`
	Sunset           string `json:"sunset"`
	Moonrise         string `json:"moonrise"`
	Moonset          string `json:"moonset"`
	MoonPhase        string `json:"moon_phase"`
	MoonIllumination int    `json:"moon_illumination"` // 0-100
}

// TimeRange is a [start, end) Unix-second window covering the target date.
type TimeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type HourlyForecast struct {
	Time          int64     `json:"time"`
	Temperature   int       `json:"temperature"`
	Condition     Condition `json:"condition"`
	Precipitation float64   `json:"precipitation"`
	Humidity      int       `json:"humidity"`
	WindSpeed     float64   `json:"wind_speed"`
}

type Alert struct {
	Type        AlertType     `json:"type"`
	Severity    AlertSeverity `json:"severity"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	StartTime   int64         `json:"start_time"`
	EndTime     int64         `json:"end_time"`
}

var _ toolcontract.Tool = (*Tool)(nil)

type Tool struct {
	writer io.Writer
	typed  toolcontract.Func[Request, *Response]
}

// New creates an offline tool. With an explicit date, output is deterministic.
// The optional writer receives call activity and remains owned by the caller.
func New(writer io.Writer) *Tool {
	if writer == nil {
		writer = io.Discard
	}
	t := &Tool{writer: writer}
	typed, err := toolcontract.NewFunc[Request, *Response](
		toolcontract.FuncConfig{
			Name:        "get_synthetic_weather",
			Description: "Generate a deterministic synthetic weather report for a location and date, optionally including hourly conditions and air quality. The result is test data, not real weather.",
		},
		t.generate,
	)
	if err != nil {
		panic(fmt.Sprintf("fakeweather: invalid static tool contract: %v", err))
	}
	t.typed = typed
	return t
}

func (t *Tool) Definition() chat.ToolDefinition { return t.typed.Definition() }

func (t *Tool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	t.log("validated_request", string(invocation.Arguments()))
	out, err := t.typed.Call(ctx, invocation)
	if err == nil {
		modelOutput, _ := out.Text()
		t.log("model_response", modelOutput)
	}
	return out, err
}

func (t *Tool) generate(_ context.Context, req Request) (*Response, error) {
	req.Location = strings.TrimSpace(req.Location)
	if req.Location == "" {
		return nil, errors.New("fakeweather: location is required")
	}
	t.log("parsed_request", fmt.Sprintf("%#v", req))

	resp, err := generate(&req)
	if err != nil {
		return nil, err
	}
	t.log("generated_response", fmt.Sprintf("%#v", resp))
	return resp, nil
}

func (t *Tool) log(key, value string) {
	_, _ = fmt.Fprintf(t.writer, "[fakeweather] %s: %s\n", key, value)
}

func (t *Tool) Unwrap() toolcontract.Tool { return t.typed }
