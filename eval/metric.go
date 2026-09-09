package eval

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// MetricName identifies one quality calculation within a namespace.
type MetricName string

// MetricNameComposite identifies the explicit score-aggregating evaluator.
const MetricNameComposite MetricName = "composite"

const metricConfigurationKey = "configuration"

// Direction describes how a raw measurement relates to quality. It is kept
// separate from Score, whose direction is always higher-is-better.
type Direction string

const (
	// DirectionUnspecified means the metric has no raw measurement direction.
	DirectionUnspecified Direction = ""
	// DirectionHigherIsBetter marks increasing raw measurements as improvement.
	DirectionHigherIsBetter Direction = "higher_is_better"
	// DirectionLowerIsBetter marks decreasing raw measurements as improvement.
	DirectionLowerIsBetter Direction = "lower_is_better"
)

func (d Direction) Validate() error {
	switch d {
	case DirectionUnspecified, DirectionHigherIsBetter, DirectionLowerIsBetter:
		return nil
	default:
		return fmt.Errorf("%w: unsupported direction %q", ErrInvalidMetric, d)
	}
}

// Metric is an immutable evaluation identity that can be copied by assignment.
// Parameters holds structured identity for calculation and decision rules.
// Unit and Direction describe optional raw measurements; normalized scores
// are always unitless and higher-is-better.
type Metric struct {
	namespace  string
	name       MetricName
	unit       string
	direction  Direction
	parameters metadata.Map
}

// MetricConfig supplies the full comparison identity of one metric.
type MetricConfig struct {
	Namespace  string
	Name       MetricName
	Unit       string
	Direction  Direction
	Parameters metadata.Map
}

// NewMetric snapshots structured parameters so later mutation cannot change
// report comparability.
func NewMetric(config MetricConfig) (Metric, error) {
	metric := Metric{
		namespace:  config.Namespace,
		name:       config.Name,
		unit:       config.Unit,
		direction:  config.Direction,
		parameters: config.Parameters.Clone(),
	}
	if err := metric.Validate(); err != nil {
		return Metric{}, err
	}
	return metric, nil
}

func (m Metric) Namespace() string        { return m.namespace }
func (m Metric) Name() MetricName         { return m.name }
func (m Metric) Unit() string             { return m.unit }
func (m Metric) Direction() Direction     { return m.direction }
func (m Metric) Parameters() metadata.Map { return m.parameters.Clone() }

func (m Metric) String() string {
	if m.namespace == "" {
		return string(m.name)
	}
	return m.namespace + "/" + string(m.name)
}

func (m Metric) Validate() error {
	if err := validateMetricPart("namespace", m.namespace, true); err != nil {
		return err
	}
	if err := validateMetricPart("name", string(m.name), false); err != nil {
		return err
	}
	if m.unit != strings.TrimSpace(m.unit) {
		return fmt.Errorf("%w: unit must not contain surrounding whitespace", ErrInvalidMetric)
	}
	if err := m.direction.Validate(); err != nil {
		return err
	}
	if err := m.parameters.Validate(); err != nil {
		return fmt.Errorf("%w: parameters: %w", ErrInvalidMetric, err)
	}
	return nil
}

type metricWire struct {
	Namespace  string       `json:"namespace,omitzero"`
	Name       MetricName   `json:"name"`
	Unit       string       `json:"unit,omitzero"`
	Direction  Direction    `json:"direction,omitzero"`
	Parameters metadata.Map `json:"parameters,omitzero"`
}

func (m Metric) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(metricWire{
		Namespace: m.namespace, Name: m.name, Unit: m.unit,
		Direction: m.direction, Parameters: m.parameters,
	})
}

func (m *Metric) UnmarshalJSON(data []byte) error {
	if m == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidMetric)
	}
	var wire metricWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidMetric, err)
	}
	value, err := NewMetric(MetricConfig(wire))
	if err != nil {
		return err
	}
	*m = value
	return nil
}

func (m Metric) identity() (string, error) {
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("%w: encode identity: %w", ErrInvalidMetric, err)
	}
	return string(encoded), nil
}

func validateMetricPart(label, value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, "/") {
		return fmt.Errorf("%w: %s must be non-empty without surrounding whitespace or slashes", ErrInvalidMetric, label)
	}
	return nil
}
