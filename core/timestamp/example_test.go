package timestamp_test

import (
	"fmt"
	"time"

	"github.com/Tangerg/scope/core/timestamp"
)

func Example() {
	instant := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	fmt.Println(timestamp.Validate(instant) == nil)
	withSeconds := instant.In(time.FixedZone("seconds", 43))
	fmt.Println(timestamp.Validate(withSeconds) == nil)
	// Output:
	// true
	// false
}
