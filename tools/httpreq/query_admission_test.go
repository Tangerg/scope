package httpreq_test

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/httpreq"
)

func TestMalformedURLQueryCannotBypassParameterOwnership(t *testing.T) {
	for _, arguments := range []string{
		`{"url":"https://allowed.example/?page=%ZZ","query":{"page":"2"}}`,
		`{"url":"https://allowed.example/?page=1;ignored=x","query":{"page":"2"}}`,
		`{"url":"https://allowed.example/?%ZZ=value","query":{"page":"2"}}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			var calls atomic.Int32
			err := callHTTPTool(t, countingTransport{calls: &calls}, arguments)
			failure, definite := tool.ClassifyFailure(err)
			if !definite || !errors.Is(failure.Cause(), httpreq.ErrInvalidURL) || calls.Load() != 0 {
				t.Fatalf("unparseable URL query reached transport: calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}
