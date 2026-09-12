package tokenizer_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/tokenizer"
)

type wordEstimator struct{}

func (wordEstimator) CountText(_ context.Context, text string) (int, error) {
	return len(strings.Fields(text)), nil
}

func Example() {
	var estimator tokenizer.TextCounter = wordEstimator{}
	count, err := estimator.CountText(context.Background(), "small stable contract")
	if err != nil {
		panic(err)
	}
	fmt.Println(count)
	// Output: 3
}
