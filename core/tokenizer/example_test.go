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
