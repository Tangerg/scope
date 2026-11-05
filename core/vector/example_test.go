package vector_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/vector"
)

func Example() {
	var coordinates vector.Coordinates[float64]
	if err := jsonv2.Unmarshal([]byte(`[0,1.5]`), &coordinates); err != nil {
		panic(err)
	}
	fmt.Println(vector.Float32(coordinates))
	err := jsonv2.Unmarshal([]byte(`[1,null]`), &coordinates)
	fmt.Println(errors.Is(err, vector.ErrInvalidCoordinates), coordinates)
	// Output:
	// [0 1.5]
	// true [0 1.5]
}
