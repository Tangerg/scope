package catalog_test

import (
	"fmt"

	"github.com/Tangerg/scope/models/catalog"
)

func ExamplePricingSchedule_Cost() {
	pricing := catalog.PricingSchedule{{
		InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: new(0.25),
	}}
	cost, err := pricing.Cost(catalog.Usage{
		InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 400,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("USD %.6f\n", cost)
	// Output: USD 0.000900
}
