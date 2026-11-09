package mariadb

import (
	"database/sql"
	"fmt"
	"testing"
)

func TestColumnRolesRequireDistinctNativeIdentities(t *testing.T) {
	for left := range 4 {
		for right := left + 1; right < 4; right++ {
			for _, alias := range []string{"same", "SAME"} {
				t.Run(fmt.Sprintf("%d-%d-%s", left, right, alias), func(t *testing.T) {
					columns := [4]string{"identity", "body", "facts", "vector"}
					columns[left], columns[right] = "same", alias
					config := StoreConfig{DB: new(sql.DB), Space: fixtureSpace, IDColumn: columns[0], ContentColumn: columns[1], MetadataColumn: columns[2], EmbeddingColumn: columns[3]}
					if err := config.Validate(); err == nil {
						t.Fatal("column alias was admitted")
					}
					if store, err := NewStore(t.Context(), config); err == nil || store != nil {
						t.Fatalf("invalid column roles reached storage: %v, %v", store, err)
					}
				})
			}
		}
	}
	config := StoreConfig{DB: new(sql.DB), Space: fixtureSpace, ContentColumn: "METADATA"}
	if err := config.Validate(); err == nil {
		t.Fatal("default metadata column alias was admitted")
	}
	config.ContentColumn = "body"
	if err := config.Validate(); err != nil {
		t.Fatalf("distinct column roles: %v", err)
	}
}
