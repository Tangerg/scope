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
