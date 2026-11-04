package vespa

import "testing"

func TestStoreConfigRejectsAnAbsentEndpoint(t *testing.T) {
	if err := (StoreConfig{}).Validate(); err == nil {
		t.Fatal("Validate accepted an absent endpoint")
	}
}

func TestEndpointRequiresAHostButAllowsTheConfiguredBasePath(t *testing.T) {
	for _, sample := range []struct {
		endpoint string
		valid    bool
	}{
		{endpoint: "http://service.example", valid: true},
		{endpoint: "https://service.example/base", valid: true},
		{endpoint: "http://:80"},
		{endpoint: "http://[]:80"},
		{endpoint: "https://service.example/base?scope=a"},
		{endpoint: "https://service.example/base#fragment"},
		{endpoint: "https://user@service.example/base"},
	} {
		t.Run(sample.endpoint, func(t *testing.T) {
			config := StoreConfig{Endpoint: sample.endpoint, SchemaName: "scope", RankingProfile: "rank", Space: fixtureSpace, Dimensions: 2}
			if err := config.Validate(); (err == nil) != sample.valid {
				t.Fatalf("endpoint %q: valid=%t, error=%v", sample.endpoint, sample.valid, err)
			}
		})
	}
}
