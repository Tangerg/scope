package vector

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
)

var ErrInvalidCoordinates = errors.New("vector: invalid coordinates")

// Coordinates contains measured, finite components. JSON null is unknown,
// so decoding it must not invent a measured zero component.
type Coordinates[T float32 | float64] []T

func (c Coordinates[T]) Validate() error {
	if len(c) == 0 {
		return fmt.Errorf("%w: missing components", ErrInvalidCoordinates)
	}
	for index, value := range c {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("%w: component %d must be finite", ErrInvalidCoordinates, index)
		}
	}
	return nil
}

func (c Coordinates[T]) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal([]T(c))
}

func (c *Coordinates[T]) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidCoordinates)
	}
	var decoded []*T
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCoordinates, err)
	}
	candidate := make(Coordinates[T], len(decoded))
	for index, value := range decoded {
		if value == nil {
			return fmt.Errorf("%w: component %d must be a number", ErrInvalidCoordinates, index)
		}
		candidate[index] = *value
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

func (Coordinates[T]) JSONSchemaModel() any { return []T{} }
