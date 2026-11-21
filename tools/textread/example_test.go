package textread_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/tools/textread"
)

func ExampleScan() {
	result, err := textread.Scan(context.Background(), strings.NewReader("zero\none\ntwo"), textread.Options{
		InputBytes: 64, LineBytes: 16, OutputBytes: 64, Offset: 1, MaxLines: 1,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Content, result.TotalLines)
	// Output:
	// one 3
}
