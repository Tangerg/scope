package tokenizer_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/tokenizer"
)

type wordCounter struct{}

func (wordCounter) CountText(_ context.Context, text string) (int, error) {
	return len(strings.Fields(text)), nil
}

func Example() {
	var counter tokenizer.TextCounter = wordCounter{}
	count, err := counter.CountText(context.Background(), "small stable contract")
	if err != nil {
		panic(err)
	}
	fmt.Println(count)
	// Output: 3
}

// A zero token limit permits batching solely by item count.
func ExampleBatchConfig_Batches() {
	config := tokenizer.BatchConfig{MaxItems: 2}
	groups, err := config.Batches(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		panic(err)
	}
	fmt.Println(groups)
	// Output: [[alpha beta] [gamma]]
}
