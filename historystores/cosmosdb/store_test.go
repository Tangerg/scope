package cosmosdb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/cosmosdb"
)

func TestNewStoreRequiresContainer(t *testing.T) {
	config := cosmosdb.StoreConfig{}
	if err := config.Validate(); err == nil {
		t.Fatal("StoreConfig.Validate should reject a nil Container")
	}
	_, err := cosmosdb.NewStore(config)
	if err == nil {
		t.Fatal("expected error when Container is nil")
	}
	if !strings.Contains(err.Error(), "container") {
		t.Fatalf("err = %v; should mention container", err)
	}
}

func TestClearFollowsEmptyPages(t *testing.T) {
	for _, failContinuation := range []bool{false, true} {
		t.Run(fmt.Sprintf("continuation_error=%t", failContinuation), func(t *testing.T) {
			queries, deletes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if request.Method == http.MethodGet {
					fmt.Fprint(writer, `{"id":"test","readableLocations":[],"writableLocations":[]}`)
					return
				}
				if request.Method == http.MethodDelete {
					deletes++
					if request.URL.Path != "/dbs/test/colls/history/docs/message" {
						t.Errorf("delete path = %q", request.URL.Path)
					}
					writer.WriteHeader(http.StatusNoContent)
					return
				}
				queries++
				switch queries {
				case 1:
					writer.Header().Set("x-ms-continuation", "next")
					fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
				case 2:
					if got := request.Header.Get("x-ms-continuation"); got != "next" {
						t.Errorf("continuation = %q, want next", got)
					}
					if failContinuation {
						writer.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(writer, `{"code":"BadRequest","message":"query failed"}`)
						return
					}
					writer.Header().Set("x-ms-continuation", "after-message")
					fmt.Fprint(writer, `{"Documents":[{"id":"message"}],"_count":1}`)
				default:
					if got := request.Header.Get("x-ms-continuation"); got != "" {
						t.Errorf("query after deletion retained continuation %q", got)
					}
					fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
				}
			}))
			defer server.Close()
			credential, err := azcosmos.NewKeyCredential("dGVzdA==")
			if err != nil {
				t.Fatal(err)
			}
			client, err := azcosmos.NewClientWithKey(server.URL, credential, nil)
			if err != nil {
				t.Fatal(err)
			}
			container, err := client.NewContainer("test", "history")
			if err != nil {
				t.Fatal(err)
			}
			store, err := cosmosdb.NewStore(cosmosdb.StoreConfig{Container: container})
			if err != nil {
				t.Fatal(err)
			}
			clearErr := store.Clear(context.Background(), history.ConversationID("conversation"))
			if failContinuation {
				if clearErr == nil || queries != 2 || deletes != 0 {
					t.Fatalf("Clear = %v; queries=%d deletes=%d, want error, 2, 0", clearErr, queries, deletes)
				}
			} else if clearErr != nil || queries != 3 || deletes != 1 {
				t.Fatalf("Clear = %v; queries=%d deletes=%d, want nil, 3, 1", clearErr, queries, deletes)
			}
		})
	}
}

func TestConversationsUsesPageableProjectionAndReturnsUniqueIDs(t *testing.T) {
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			fmt.Fprint(writer, `{"id":"test","readableLocations":[],"writableLocations":[]}`)
			return
		}
		var query struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(request.Body).Decode(&query); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if query.Query != "SELECT VALUE c.conversation_id FROM c" {
			t.Errorf("query = %q, want a pageable cross-partition projection", query.Query)
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"code":"BadRequest","message":"unsupported cross-partition query"}`)
			return
		}
		queries++
		switch queries {
		case 1:
			writer.Header().Set("x-ms-continuation", "empty-page")
			fmt.Fprint(writer, `{"Documents":["z","a","z"],"_count":3}`)
		case 2:
			writer.Header().Set("x-ms-continuation", "last-page")
			fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
		case 3:
			fmt.Fprint(writer, `{"Documents":["a","b"],"_count":2}`)
		default:
			t.Errorf("unexpected query %d", queries)
			fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
		}
	}))
	defer server.Close()
	credential, err := azcosmos.NewKeyCredential("dGVzdA==")
	if err != nil {
		t.Fatal(err)
	}
	client, err := azcosmos.NewClientWithKey(server.URL, credential, nil)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer("test", "history")
	if err != nil {
		t.Fatal(err)
	}
	store, err := cosmosdb.NewStore(cosmosdb.StoreConfig{Container: container})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.Conversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []history.ConversationID{"a", "b", "z"}; !slices.Equal(ids, want) || queries != 3 {
		t.Fatalf("Conversations = %v, queries=%d; want %v, 3", ids, queries, want)
	}
}
