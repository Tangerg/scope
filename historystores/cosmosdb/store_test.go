package cosmosdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/cosmosdb"
)

func TestNewStoreRequiresContainer(t *testing.T) {
	config := cosmosdb.StoreConfig{}
	if err := config.Validate(); err == nil {
		t.Fatal("StoreConfig.Validate should reject a nil Container")
	}
	_, err := cosmosdb.NewStore(t.Context(), config)
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
			store := newTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
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
			})
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
	store := newTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
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
	})
	ids, err := store.Conversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []history.ConversationID{"a", "b", "z"}; !slices.Equal(ids, want) || queries != 3 {
		t.Fatalf("Conversations = %v, queries=%d; want %v, 3", ids, queries, want)
	}
}

// newTestStore wires a store against a fake service that answers construction
// itself, leaving handler to serve only the operation under test.
func newTestStore(t *testing.T, handler http.HandlerFunc) *cosmosdb.Store {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			fmt.Fprint(writer, getResponseBody(request))
			return
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)

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
	store, err := cosmosdb.NewStore(t.Context(), cosmosdb.StoreConfig{Container: container})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// Write used to create one item per message, which Cosmos applies one at a
// time: a failure partway left a conversation holding a prefix of the batch
// under an error that named only the message it stopped on. Every message in
// one Write shares the conversation id, which is the partition key, so the
// whole batch meets Cosmos's rule for a transactional batch and earns "if any
// operation fails, the entire transaction is rolled back".
func TestWriteSendsOneTransactionalBatch(t *testing.T) {
	var operations []struct {
		OperationType string          `json:"operationType"`
		ResourceBody  json.RawMessage `json:"resourceBody"`
	}
	posts := 0
	store := newTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
		posts++
		if got := request.Header.Get("x-ms-cosmos-is-batch-request"); got != "True" {
			t.Errorf("batch header = %q, want True", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&operations); err != nil {
			t.Error(err)
		}
		fmt.Fprint(writer, `[{"statusCode":201},{"statusCode":201}]`)
	})

	messages := []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("first")),
		chat.NewUserMessage(chat.NewTextPart("second")),
	}
	if outcome, err := store.Write(t.Context(), history.ConversationID("conversation"), messages...); err != nil || outcome != (history.WriteOutcome{Accepted: len(messages)}) {
		t.Fatalf("Write: %v", err)
	}
	if posts != 1 {
		t.Fatalf("requests = %d, want the whole write in one batch", posts)
	}
	if len(operations) != len(messages) {
		t.Fatalf("operations = %d, want %d", len(operations), len(messages))
	}
	for index, operation := range operations {
		if operation.OperationType != "Create" {
			t.Errorf("operations[%d] type = %q, want Create", index, operation.OperationType)
		}
	}
}

// A rolled-back batch arrives as HTTP 207, which the SDK reports with a nil
// error and Success false. Reading the call as the answer would return nil for
// a conversation Cosmos stored nothing of.
func TestWriteReportsARolledBackBatch(t *testing.T) {
	store := newTestStore(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusMultiStatus)
		// 424 is "failed dependency": rolled back rather than at fault. The
		// cause is the first operation carrying anything else.
		fmt.Fprint(writer, `[{"statusCode":424},{"statusCode":409}]`)
	})

	outcome, err := store.Write(t.Context(), history.ConversationID("conversation"),
		chat.NewUserMessage(chat.NewTextPart("first")),
		chat.NewUserMessage(chat.NewTextPart("second")))
	if err == nil || outcome != (history.WriteOutcome{}) {
		t.Fatal("Write() = nil error, want the rolled-back batch reported")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("Write() = %v, want an error naming the operation that failed", err)
	}
}

// Cosmos caps a transactional batch at 100 operations. Splitting a larger
// write across batches would give up the atomicity that makes it reportable,
// so the choice is handed back rather than made quietly.
func TestWriteRefusesMoreMessagesThanOneBatchCarries(t *testing.T) {
	store := newTestStore(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Write reached the service with an oversized batch")
	})

	messages := make([]chat.Message, cosmosdb.MaxMessagesPerWrite+1)
	for index := range messages {
		messages[index] = chat.NewUserMessage(chat.NewTextPart("message"))
	}
	outcome, err := store.Write(t.Context(), history.ConversationID("conversation"), messages...)
	if err == nil || outcome != (history.WriteOutcome{}) {
		t.Fatal("Write() = nil error, want the oversized batch refused")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(cosmosdb.MaxMessagesPerWrite)) {
		t.Fatalf("Write() = %v, want an error naming the limit", err)
	}
}

// getResponseBody answers the two GETs construction makes: the account
// metadata the SDK reads first, then the container definition NewStore reads to
// confirm the partition-key path.
func getResponseBody(request *http.Request) string {
	if strings.Contains(request.URL.Path, "/colls/") {
		return `{"id":"history","partitionKey":{"kind":"Hash","paths":["` + cosmosdb.PartitionKeyPath + `"]}}`
	}
	return `{"id":"test","readableLocations":[],"writableLocations":[]}`
}

// The partition-key requirement used to be an obligation the config stated in
// capitals and nothing checked. Cosmos does reject the first write itself, but
// that arrives far from the wiring that chose the container.
func TestNewStoreRefusesAContainerPartitionedElsewhere(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "/colls/") {
			fmt.Fprint(writer, `{"id":"history","partitionKey":{"kind":"Hash","paths":["/tenant"]}}`)
			return
		}
		fmt.Fprint(writer, `{"id":"test","readableLocations":[],"writableLocations":[]}`)
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

	_, err = cosmosdb.NewStore(t.Context(), cosmosdb.StoreConfig{Container: container})
	if !errors.Is(err, cosmosdb.ErrIncompatibleContainer) {
		t.Fatalf("NewStore() = %v, want ErrIncompatibleContainer", err)
	}
	if !strings.Contains(err.Error(), cosmosdb.PartitionKeyPath) {
		t.Fatalf("NewStore() = %v, want an error naming %s", err, cosmosdb.PartitionKeyPath)
	}
}

func TestWriteReportsUncertainOutcomeForMalformedAcknowledgment(t *testing.T) {
	store := newTestStore(t, func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `not a batch acknowledgment`)
	})
	outcome, err := store.Write(t.Context(), "conversation", chat.NewUserMessage(chat.NewTextPart("first")))
	if err == nil || outcome != (history.WriteOutcome{Uncertain: true}) {
		t.Fatalf("outcome=%+v error=%v", outcome, err)
	}
}
