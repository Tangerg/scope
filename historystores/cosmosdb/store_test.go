package cosmosdb_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/storetest"
	"github.com/Tangerg/scope/historystores/cosmosdb"
)

type nativeItem struct {
	body json.RawMessage
	etag string
}

type batchOperation struct {
	OperationType string          `json:"operationType"`
	ID            string          `json:"id"`
	ResourceBody  json.RawMessage `json:"resourceBody"`
	IfMatch       string          `json:"ifMatch"`
}

type projectedItem struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ConversationID string `json:"conversation_id"`
	AppendID       string `json:"append_id"`
	Digest         string `json:"digest"`
	Sequence       string `json:"seq"`
	Messages       string `json:"messages"`
	Closed         bool   `json:"closed"`
	TTL            int    `json:"ttl"`
}

// The fixture implements Cosmos item and conditional-transaction primitives;
// append identities, digests, ordering, and closure decisions remain in Store.
type cosmosPeer struct {
	t                      *testing.T
	mutex                  sync.Mutex
	items                  map[string]map[string]nativeItem
	version                int
	operations             [][]batchOperation
	rejectStrong           bool
	partitionPath          string
	loseAcknowledgment     bool
	failDelete             bool
	emptyCleanupPage       bool
	failContinuation       bool
	closeBeforeReplacement bool
	acknowledgment         string
	acknowledgmentStatus   int
}

func (c *cosmosPeer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodGet && request.URL.Path == "/" {
		fmt.Fprint(writer, `{"id":"test","readableLocations":[],"writableLocations":[],"enableMultipleWriteLocations":false,"consistencyPolicy":{"defaultConsistencyLevel":"Strong"}}`)
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/dbs/test/colls/history" {
		path := c.partitionPath
		if path == "" {
			path = cosmosdb.PartitionKeyPath
		}
		fmt.Fprintf(writer, `{"id":"history","_rid":"container","partitionKey":{"kind":"Hash","paths":[%q]}}`, path)
		return
	}
	if request.Header.Get("x-ms-consistency-level") != "Strong" {
		c.t.Errorf("missing Strong consistency: %s %s", request.Method, request.URL.Path)
	}
	if c.rejectStrong {
		writeNativeError(writer, http.StatusBadRequest, "stronger consistency than account")
		return
	}
	var partitionKeys []string
	if header := request.Header.Get("x-ms-documentdb-partitionkey"); header != "" {
		if err := jsonv2.Unmarshal([]byte(header), &partitionKeys); err != nil {
			c.t.Error(err)
			writeNativeError(writer, http.StatusBadRequest, "invalid partition key")
			return
		}
	}
	partition := ""
	if len(partitionKeys) > 0 {
		partition = partitionKeys[0]
	}
	if request.Method == http.MethodPost && request.Header.Get("x-ms-documentdb-query") == "True" {
		c.query(writer, request, partition)
		return
	}
	if request.Method == http.MethodPost && request.Header.Get("x-ms-cosmos-is-batch-request") == "True" {
		c.transaction(writer, request, partition)
		return
	}
	id := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
	switch request.Method {
	case http.MethodGet:
		item, found := c.items[partition][id]
		if !found {
			writeNativeError(writer, http.StatusNotFound, "missing item")
			return
		}
		writer.Header().Set("ETag", item.etag)
		writer.Write(item.body)
	case http.MethodDelete:
		if c.failDelete {
			c.failDelete = false
			writeNativeError(writer, http.StatusBadRequest, "cleanup failed")
			return
		}
		if _, found := c.items[partition][id]; !found {
			writeNativeError(writer, http.StatusNotFound, "missing item")
			return
		}
		delete(c.items[partition], id)
		writer.WriteHeader(http.StatusNoContent)
	default:
		c.t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		writeNativeError(writer, http.StatusBadRequest, "unexpected request")
	}
}

func (c *cosmosPeer) transaction(writer http.ResponseWriter, request *http.Request, partition string) {
	var operations []batchOperation
	if err := jsonv2.UnmarshalRead(request.Body, &operations); err != nil {
		c.t.Error(err)
		writeNativeError(writer, http.StatusBadRequest, "invalid transaction")
		return
	}
	c.operations = append(c.operations, operations)
	if c.closeBeforeReplacement && len(operations) > 0 && operations[0].OperationType == "Replace" {
		c.closeBeforeReplacement = false
		item := c.items[partition]["conversation"]
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(item.body, &fields); err != nil {
			c.t.Error(err)
		}
		fields["closed"] = json.RawMessage("true")
		body, err := jsonv2.Marshal(fields)
		if err != nil {
			c.t.Error(err)
		}
		c.version++
		c.items[partition]["conversation"] = nativeItem{body: body, etag: fmt.Sprint(c.version)}
	}
	next := make(map[string]nativeItem)
	for id, item := range c.items[partition] {
		next[id] = item
	}
	statuses := make([]map[string]int, len(operations))
	failure := -1
	for index, operation := range operations {
		var document projectedItem
		if err := jsonv2.Unmarshal(operation.ResourceBody, &document); err != nil {
			c.t.Error(err)
			failure = index
			statuses[index] = map[string]int{"statusCode": http.StatusBadRequest}
			break
		}
		status := http.StatusCreated
		switch operation.OperationType {
		case "Create":
			if _, exists := next[document.ID]; exists {
				status = http.StatusConflict
			}
		case "Replace":
			item, exists := next[operation.ID]
			if !exists {
				status = http.StatusNotFound
			} else if operation.IfMatch == "" || operation.IfMatch != item.etag {
				status = http.StatusPreconditionFailed
			} else {
				status = http.StatusOK
			}
		default:
			status = http.StatusBadRequest
		}
		statuses[index] = map[string]int{"statusCode": status}
		if status >= http.StatusBadRequest {
			failure = index
			break
		}
		c.version++
		next[document.ID] = nativeItem{body: bytes.Clone(operation.ResourceBody), etag: fmt.Sprint(c.version)}
	}
	if failure >= 0 {
		for index := range statuses {
			if index != failure {
				statuses[index] = map[string]int{"statusCode": http.StatusFailedDependency}
			}
		}
		writer.WriteHeader(http.StatusMultiStatus)
		writeNativeJSON(writer, statuses)
		return
	}
	c.items[partition] = next
	if c.acknowledgment != "" {
		if c.acknowledgmentStatus != 0 {
			writer.WriteHeader(c.acknowledgmentStatus)
		}
		fmt.Fprint(writer, c.acknowledgment)
		return
	}
	if c.loseAcknowledgment {
		c.loseAcknowledgment = false
		fmt.Fprint(writer, "acknowledgment lost")
		return
	}
	writeNativeJSON(writer, statuses)
}

func (c *cosmosPeer) query(writer http.ResponseWriter, request *http.Request, partition string) {
	var query struct {
		Query      string `json:"query"`
		Parameters []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"parameters"`
	}
	if err := jsonv2.UnmarshalRead(request.Body, &query); err != nil {
		c.t.Error(err)
		writeNativeError(writer, http.StatusBadRequest, "invalid query")
		return
	}
	if query.Query == "SELECT VALUE c.id FROM c WHERE false" {
		writeNativeJSON(writer, map[string]any{"Documents": []any{}})
		return
	}
	parameters := map[string]string{}
	for _, parameter := range query.Parameters {
		parameters[parameter.Name] = parameter.Value
	}
	var documents []json.RawMessage
	for key, items := range c.items {
		if partition != "" && key != partition {
			continue
		}
		for _, item := range items {
			var projected projectedItem
			if err := jsonv2.Unmarshal(item.body, &projected); err != nil {
				c.t.Error(err)
				continue
			}
			if projected.Kind != parameters["@kind"] {
				continue
			}
			switch query.Query {
			case "SELECT * FROM c WHERE c.kind = @kind AND c.seq <= @sequence":
				if projected.Sequence <= parameters["@sequence"] {
					documents = append(documents, bytes.Clone(item.body))
				}
			case "SELECT VALUE c.conversation_id FROM c WHERE c.kind = @kind AND c.closed = false":
				if !projected.Closed {
					raw, _ := jsonv2.Marshal(projected.ConversationID)
					documents = append(documents, raw)
				}
			case "SELECT VALUE c.id FROM c WHERE c.kind = @kind":
				raw, _ := jsonv2.Marshal(projected.ID)
				documents = append(documents, raw)
			default:
				c.t.Errorf("unexpected query %q", query.Query)
			}
		}
	}
	cleanup := query.Query == "SELECT VALUE c.id FROM c WHERE c.kind = @kind"
	if cleanup && len(documents) > 0 && c.emptyCleanupPage {
		if request.Header.Get("x-ms-continuation") == "" {
			writer.Header().Set("x-ms-continuation", "next")
			documents = nil
		} else if c.failContinuation {
			c.failContinuation = false
			writeNativeError(writer, http.StatusBadRequest, "continuation failed")
			return
		}
	}
	if documents == nil {
		documents = []json.RawMessage{}
	}
	writeNativeJSON(writer, map[string]any{"Documents": documents})
}

func (c *cosmosPeer) payloadCount(conversation string) int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	count := 0
	for id := range c.items[conversation] {
		if id != "conversation" {
			count++
		}
	}
	return count
}

func writeNativeJSON(writer http.ResponseWriter, value any) {
	body, err := jsonv2.Marshal(value)
	if err != nil {
		panic(err)
	}
	writer.Write(body)
}

func writeNativeError(writer http.ResponseWriter, status int, message string) {
	writer.WriteHeader(status)
	writeNativeJSON(writer, map[string]string{"code": http.StatusText(status), "message": message})
}

func newPeer(t *testing.T) *cosmosPeer {
	return &cosmosPeer{t: t, items: make(map[string]map[string]nativeItem)}
}

func testContainer(t *testing.T, peer *cosmosPeer) *azcosmos.ContainerClient {
	t.Helper()
	server := httptest.NewServer(peer)
	t.Cleanup(server.Close)
	credential, err := azcosmos.NewKeyCredential("dGVzdA==")
	if err != nil {
		t.Fatal(err)
	}
	client, err := azcosmos.NewClientWithKey(server.URL, credential, &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	container, err := client.NewContainer("test", "history")
	if err != nil {
		t.Fatal(err)
	}
	return container
}

func testStore(t *testing.T, peer *cosmosPeer) *cosmosdb.Store {
	t.Helper()
	store, err := cosmosdb.NewStore(t.Context(), cosmosdb.StoreConfig{Container: testContainer(t, peer)})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func namedBatch(t *testing.T, id history.AppendID, texts ...string) history.Batch {
	t.Helper()
	messages := make([]chat.Message, len(texts))
	for index, text := range texts {
		messages[index] = chat.NewUserMessage(chat.NewTextPart(text))
	}
	batch, err := history.NewBatch("conversation", id, messages...)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestNewStoreRequiresVerifiedContainerCapabilities(t *testing.T) {
	if _, err := cosmosdb.NewStore(t.Context(), cosmosdb.StoreConfig{}); err == nil {
		t.Fatal("nil container accepted")
	}
	for _, test := range []struct {
		name, path string
		strong     bool
	}{
		{name: "partition", path: "/tenant"},
		{name: "consistency", strong: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newPeer(t)
			peer.partitionPath = test.path
			peer.rejectStrong = test.strong
			store, err := cosmosdb.NewStore(t.Context(), cosmosdb.StoreConfig{Container: testContainer(t, peer)})
			if store != nil || !errors.Is(err, cosmosdb.ErrIncompatibleContainer) {
				t.Fatalf("store=%v error=%v", store, err)
			}
		})
	}
}

func TestAtomicContractThroughNativeSDK(t *testing.T) {
	storetest.RunAtomic(t, testStore(t, newPeer(t)))
}

func TestAppendCommitsCanonicalBatchWithGate(t *testing.T) {
	peer := newPeer(t)
	store := testStore(t, peer)
	batch := namedBatch(t, "append", "first", "second")
	if receipt, err := store.Append(t.Context(), batch); err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("receipt=%+v error=%v", receipt, err)
	}
	if receipt, err := store.Append(t.Context(), batch); err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("duplicate=%+v error=%v", receipt, err)
	}
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	if len(peer.operations) != 2 || len(peer.operations[0]) != 2 || len(peer.operations[1]) != 1 {
		t.Fatalf("operations=%v", peer.operations)
	}
	first := peer.operations[0]
	var payload projectedItem
	if err := jsonv2.Unmarshal(first[1].ResourceBody, &payload); err != nil {
		t.Fatal(err)
	}
	if first[0].OperationType != "Create" || first[1].OperationType != "Create" || payload.Messages != string(batch.EncodedMessages()) || payload.Digest != batch.Digest() || payload.TTL != -1 {
		t.Fatalf("payload=%+v operations=%v", payload, first)
	}
	if repeated := peer.operations[1][0]; repeated.OperationType != "Replace" || repeated.IfMatch == "" {
		t.Fatalf("duplicate bypassed gate CAS: %+v", repeated)
	}
}

func TestLostAcknowledgmentRetriesTheSameDurableIdentity(t *testing.T) {
	peer := newPeer(t)
	store := testStore(t, peer)
	peer.mutex.Lock()
	peer.loseAcknowledgment = true
	peer.mutex.Unlock()
	batch := namedBatch(t, "same", "hello")
	if receipt, err := store.Append(t.Context(), batch); err == nil || receipt != (history.Receipt{}) {
		t.Fatalf("lost acknowledgment=%+v, %v", receipt, err)
	}
	if receipt, err := store.Append(t.Context(), batch); err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("retry=%+v, %v", receipt, err)
	}
	if count := peer.payloadCount("conversation"); count != 1 {
		t.Fatalf("payloads=%d", count)
	}
	if messages, err := store.Read(t.Context(), "conversation"); err != nil || !reflect.DeepEqual(messages, batch.Messages()) {
		t.Fatalf("messages=%v error=%v", messages, err)
	}
}

func TestConcurrentClosureWinsBeforeDuplicateReceipt(t *testing.T) {
	peer := newPeer(t)
	store := testStore(t, peer)
	batch := namedBatch(t, "same", "hello")
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	peer.mutex.Lock()
	peer.closeBeforeReplacement = true
	peer.mutex.Unlock()
	if receipt, err := store.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) || receipt != (history.Receipt{}) {
		t.Fatalf("closed duplicate=%+v, %v", receipt, err)
	}
}

func TestCleanupFailureRetainsClosureAndCanResume(t *testing.T) {
	for _, continuationFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(continuationFailure), func(t *testing.T) {
			peer := newPeer(t)
			store := testStore(t, peer)
			batch := namedBatch(t, "same", "hello")
			if _, err := store.Append(t.Context(), batch); err != nil {
				t.Fatal(err)
			}
			peer.mutex.Lock()
			peer.emptyCleanupPage = true
			peer.failContinuation = continuationFailure
			peer.failDelete = !continuationFailure
			peer.mutex.Unlock()
			if err := store.Clear(t.Context(), "conversation"); err == nil {
				t.Fatal("cleanup failure was hidden")
			}
			if _, err := store.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) {
				t.Fatalf("cleanup failure reopened append: %v", err)
			}
			if messages, err := store.Read(t.Context(), "conversation"); err != nil || messages == nil || len(messages) != 0 {
				t.Fatalf("closed read=%v, %v", messages, err)
			}
			if ids, err := store.Conversations(t.Context()); err != nil || slices.Contains(ids, history.ConversationID("conversation")) {
				t.Fatalf("closed listing=%v, %v", ids, err)
			}
			if err := store.Clear(t.Context(), "conversation"); err != nil {
				t.Fatal(err)
			}
			if count := peer.payloadCount("conversation"); count != 0 {
				t.Fatalf("remaining payloads=%d", count)
			}
		})
	}
}

func TestAppendRejectsUnrepresentableIdentityAndOversizedPayloadBeforeIO(t *testing.T) {
	peer := newPeer(t)
	store := testStore(t, peer)
	for _, batch := range []history.Batch{namedBatch(t, history.AppendID(strings.Repeat("x", 2000)), "hello"), namedBatch(t, "large", strings.Repeat("x", 2<<20))} {
		if receipt, err := store.Append(t.Context(), batch); err == nil || receipt != (history.Receipt{}) {
			t.Fatalf("invalid physical append accepted: %+v, %v", receipt, err)
		}
	}
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	if len(peer.operations) != 0 {
		t.Fatalf("preflight dispatched %d transactions", len(peer.operations))
	}
}

func TestOpaqueAppendIDsDoNotBecomeNativeItemPaths(t *testing.T) {
	peer := newPeer(t)
	store := testStore(t, peer)
	batch := namedBatch(t, "a/b?c#d", "hello")
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	id := "append:" + base64.RawURLEncoding.EncodeToString([]byte(batch.ID()))
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	if _, found := peer.items["conversation"][id]; !found {
		t.Fatalf("stable escaped ID %q missing", id)
	}
}

func TestMalformedTransactionEvidenceDoesNotAuthorizeRetry(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{name: "missing operation", body: `[{"statusCode":201}]`},
		{name: "success with rejected operation", body: `[{"statusCode":201},{"statusCode":400}]`},
		{name: "multiple rollback causes", body: `[{"statusCode":412},{"statusCode":400}]`, status: http.StatusMultiStatus},
		{name: "rollback without cause", body: `[{"statusCode":424},{"statusCode":424}]`, status: http.StatusMultiStatus},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newPeer(t)
			store := testStore(t, peer)
			peer.mutex.Lock()
			peer.acknowledgment = test.body
			peer.acknowledgmentStatus = test.status
			peer.mutex.Unlock()
			if receipt, err := store.Append(t.Context(), namedBatch(t, "append", "hello")); err == nil || receipt != (history.Receipt{}) {
				t.Fatalf("malformed evidence accepted: %+v, %v", receipt, err)
			}
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			if len(peer.operations) != 1 {
				t.Fatalf("uncertain attempt repeated %d times", len(peer.operations))
			}
		})
	}
}
