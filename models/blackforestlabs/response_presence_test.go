package blackforestlabs

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/media"
)

func TestImageMetadataPreservesReportedZero(t *testing.T) {
	value, err := media.NewBytes("image/png", []byte("PNG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{"", `,"seed":0,"duration":0`} {
		t.Run(fields, func(t *testing.T) {
			var final pollResult
			if err := jsonv2.Unmarshal([]byte(`{"status":"Ready","result":{"sample":"https://example.com/image.png"`+fields+`}}`), &final); err != nil {
				t.Fatal(err)
			}
			response, err := mapImageResponse(value, &asyncResponse{ID: "task"}, &final)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"blackforestlabs/seed", "blackforestlabs/duration_ms"} {
				raw, exists := response.Outputs[0].Metadata[key]
				if exists != (fields != "") || exists && string(raw) != "0" {
					t.Fatalf("%s=%s present=%t", key, raw, exists)
				}
			}
		})
	}
}
