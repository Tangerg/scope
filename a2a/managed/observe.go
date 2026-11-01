package managed

import (
	"iter"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
)

func failStream(err error) iter.Seq2[sdk.Event, error] {
	return func(yield func(sdk.Event, error) bool) { yield(nil, err) }
}
