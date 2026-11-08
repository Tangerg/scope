package deepgram

import "testing"

func TestAPIResponseLimit(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured int64
		want       int64
	}{
		{name: "default", want: DefaultMaxResponseBytes},
		{name: "explicit", configured: 64, want: 64},
		{name: "negative", configured: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := newAPI(t.Context(), apiConfig{APIKey: "test-key", MaxResponseBytes: test.configured})
			if test.configured < 0 {
				if adapter != nil || err == nil {
					t.Fatalf("negative response limit admitted: adapter=%v error=%v", adapter, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if adapter.maxResponseBytes != test.want || int64(adapter.http.ResponseBodyLimit) != test.want {
				t.Fatalf("response limit: speech=%d transcription=%d, want %d", adapter.maxResponseBytes, adapter.http.ResponseBodyLimit, test.want)
			}
		})
	}
}
