//go:build integration

package neo4j_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/Tangerg/scope/core/history/storetest"
	neo4jstore "github.com/Tangerg/scope/historystores/neo4j"
)

func TestNativeAppendAndClosure(t *testing.T) {
	uri := os.Getenv("SCOPE_HISTORY_NEO4J_URI")
	if uri == "" {
		t.Skip("SCOPE_HISTORY_NEO4J_URI is required")
	}
	driver, err := neo4j.NewDriver(uri, neo4j.BasicAuth(os.Getenv("SCOPE_HISTORY_NEO4J_USER"), os.Getenv("SCOPE_HISTORY_NEO4J_PASSWORD"), ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	label := "History_" + strings.ToLower(rand.Text())
	conversationLabel := label + "Conversation"
	store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: driver, AppendLabel: label, ConversationLabel: conversationLabel, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		for _, query := range []string{"MATCH (n:" + label + ") DELETE n", "MATCH (n:" + conversationLabel + ") DELETE n", "DROP CONSTRAINT " + label + "_id_unique IF EXISTS", "DROP CONSTRAINT " + conversationLabel + "_id_unique IF EXISTS", "DROP INDEX " + label + "_order IF EXISTS"} {
			if _, err := neo4j.ExecuteQuery(ctx, driver, query, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(neo4jstore.DefaultDatabase)); err != nil {
				t.Error(err)
			}
		}
	})
	storetest.RunAtomic(t, store)
}
