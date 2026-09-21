package neo4j

import (
	"strings"
	"testing"

	neo4j "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type nilClient struct{ neo4j.DriverWithContext }

func TestConfigRejectsTypedNilClient(t *testing.T) {
	var client *nilClient
	config := StoreConfig{Driver: client}
	err := config.Validate()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "driver") {
		t.Fatalf("typed nil client error = %v", err)
	}
}
