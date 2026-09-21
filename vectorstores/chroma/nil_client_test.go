package chroma

import (
	"strings"
	"testing"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
)

type nilClient struct{ v2.Client }

func TestConfigRejectsTypedNilClient(t *testing.T) {
	var client *nilClient
	config := StoreConfig{Client: client}
	err := config.Validate()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "client") {
		t.Fatalf("typed nil client error = %v", err)
	}
}
