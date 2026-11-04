package azureaisearch

import "testing"

func TestEndpointRejectsEmptyQueryAndFragment(t *testing.T) {
	fixture := newProtocolFixture(t)
	for _, suffix := range []string{"?", "#", "/?", "/#"} {
		config := fixture.config()
		config.Endpoint += suffix
		if err := config.Validate(); err == nil {
			t.Fatalf("endpoint %q can absorb the index resource path", config.Endpoint)
		}
	}
}

func TestEndpointRequiresAHostAndAnUnqualifiedServiceOrigin(t *testing.T) {
	fixture := newProtocolFixture(t)
	for _, sample := range []struct {
		endpoint string
		valid    bool
	}{
		{endpoint: "https://service.example", valid: true},
		{endpoint: "http://service.example/", valid: true},
		{endpoint: "http://:80"},
		{endpoint: "http://[]:80"},
		{endpoint: "https://service.example/base"},
		{endpoint: "https://service.example?scope=a"},
		{endpoint: "https://service.example#fragment"},
		{endpoint: "https://user@service.example"},
	} {
		t.Run(sample.endpoint, func(t *testing.T) {
			config := fixture.config()
			config.Endpoint = sample.endpoint
			if err := config.Validate(); (err == nil) != sample.valid {
				t.Fatalf("endpoint %q: valid=%t, error=%v", sample.endpoint, sample.valid, err)
			}
		})
	}
}
