package neo4j_test

import (
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"

	neo4jstore "github.com/Tangerg/scope/historystores/neo4j"
)

// stubDriver is a non-nil but never-used Driver for tests
// that only exercise validation. The driver is never queried.
func stubDriver() neo4j.Driver {
	drv, _ := neo4j.NewDriver("bolt://127.0.0.1:0", neo4j.NoAuth())
	return drv
}

func TestNewStoreRequiresDriver(t *testing.T) {
	config := neo4jstore.StoreConfig{}
	if err := config.Validate(); err == nil {
		t.Fatal("StoreConfig.Validate should reject a nil Driver")
	}
	_, err := neo4jstore.NewStore(t.Context(), config)
	if err == nil {
		t.Fatal("expected error when Driver is nil")
	}
	if !strings.Contains(err.Error(), "driver") {
		t.Fatalf("err = %v; should mention driver", err)
	}
}

func TestNewStoreRejectsBadLabel(t *testing.T) {
	_, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{
		Driver: stubDriver(),
		Label:  "Bad-Label",
	})
	if err == nil {
		t.Fatal("expected error on label with hyphen")
	}
}

func TestNewStoreAcceptsDefaults(t *testing.T) {
	_, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: stubDriver()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
