package mongodb

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/xoptions"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

type recordedCommand struct {
	name     string
	document bson.Raw
}

func transactionStore(t *testing.T, responses ...bson.D) (*Store, *[]recordedCommand) {
	t.Helper()
	deployment := drivertest.NewMockDeployment(responses...)
	commands := []recordedCommand{}
	config := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, started *event.CommandStartedEvent) {
		commands = append(commands, recordedCommand{name: started.CommandName, document: bytes.Clone(started.Command)})
	}})
	if err := xoptions.SetInternalClientOptions(config, "deployment", deployment); err != nil {
		t.Fatal(err)
	}
	client, err := mongo.Connect(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deployment.AddResponses(commandOK())
		if closeErr := client.Disconnect(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Collection: client.Database("scope").Collection("history")})
	if err != nil {
		t.Fatal(err)
	}
	return store, &commands
}

func commandOK() bson.D { return bson.D{{Key: "ok", Value: 1}} }
func writeOK() bson.D {
	return bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}, {Key: "nModified", Value: 1}}
}
func gateResponse(gate conversationGate) bson.D {
	return bson.D{{Key: "ok", Value: 1}, {Key: "value", Value: gate}}
}
func cursorResponse(records ...any) bson.D {
	documents := bson.A{}
	documents = append(documents, records...)
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.D{{Key: "id", Value: int64(0)}, {Key: "ns", Value: "scope.history"}, {Key: "firstBatch", Value: documents}}}}
}
func appendBatch(t *testing.T) history.Batch {
	t.Helper()
	batch, err := history.NewBatch("conversation", "stable", chat.NewUserMessage(chat.NewTextPart("first")), chat.NewAssistantMessage(chat.NewTextPart("second")))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func requireTransaction(t *testing.T, commands []recordedCommand, names ...string) {
	t.Helper()
	actual := make([]string, len(commands))
	for index, command := range commands {
		actual[index] = command.name
	}
	if !slices.Equal(actual, names) {
		t.Fatalf("commands = %v, want %v", actual, names)
	}
	first := commands[1].document
	if !first.Lookup("startTransaction").Boolean() || first.Lookup("readConcern").Document().Lookup("level").StringValue() != "snapshot" {
		t.Fatalf("transaction did not start with snapshot: %v", first)
	}
	for _, command := range commands[1:] {
		if command.document.Lookup("autocommit").Boolean() {
			t.Fatalf("autocommit enabled: %v", command)
		}
		if !bytes.Equal(command.document.Lookup("lsid").Document(), first.Lookup("lsid").Document()) || command.document.Lookup("txnNumber").Int64() != first.Lookup("txnNumber").Int64() {
			t.Fatalf("transaction identity changed: %v", command)
		}
	}
	last := commands[len(commands)-1]
	if last.name == "commitTransaction" && last.document.Lookup("writeConcern").Document().Lookup("w").StringValue() != "majority" {
		t.Fatalf("commit lacks majority acknowledgment: %v", last.document)
	}
}

func TestAppendPublishesBatchAndReceiptInOneNativeTransaction(t *testing.T) {
	batch := appendBatch(t)
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{}), cursorResponse(), writeOK(), writeOK(), commandOK())
	receipt, err := store.Append(t.Context(), batch)
	if err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("Append = %+v, %v", receipt, err)
	}
	requireTransaction(t, *commands, "update", "findAndModify", "find", "insert", "update", "commitTransaction")
	raw := (*commands)[3].document.Lookup("documents").Array().Index(0).Document()
	var record appendRecord
	if err := bson.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.ID.Data, batchID(batch).Data) || record.AppendID != batch.ID().String() || record.Digest != batch.Digest() || record.Messages != string(batch.EncodedMessages()) || record.Sequence != 1 {
		t.Fatalf("persisted record diverged from Batch: %+v", record)
	}
}

func TestAppendRetryUsesReceiptAndRejectsConflicts(t *testing.T) {
	batch := appendBatch(t)
	for _, test := range []struct {
		name   string
		digest string
		want   error
	}{
		{name: "same batch", digest: batch.Digest()},
		{name: "changed payload", digest: "different", want: history.ErrAppendConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{NextSequence: 1}), cursorResponse(appendRecord{Digest: test.digest}), commandOK())
			receipt, err := store.Append(t.Context(), batch)
			if !errors.Is(err, test.want) {
				t.Fatalf("Append = %+v, %v", receipt, err)
			}
			final := "commitTransaction"
			if test.want != nil {
				final = "abortTransaction"
				if receipt != (history.Receipt{}) {
					t.Fatal("conflict returned a receipt")
				}
			} else if receipt.ValidateFor(batch) != nil {
				t.Fatal("retry lost its receipt")
			}
			requireTransaction(t, *commands, "update", "findAndModify", "find", final)
		})
	}
}

func TestAppendChecksPermanentClosureBeforeReceipt(t *testing.T) {
	batch := appendBatch(t)
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{Closed: true}), commandOK())
	receipt, err := store.Append(t.Context(), batch)
	if receipt != (history.Receipt{}) || !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("Append = %+v, %v", receipt, err)
	}
	requireTransaction(t, *commands, "update", "findAndModify", "abortTransaction")
}

func TestAppendFailureAbortsWithoutConfirmation(t *testing.T) {
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{}), cursorResponse(), bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 2}, {Key: "errmsg", Value: "rejected insert"}}, commandOK())
	receipt, err := store.Append(t.Context(), appendBatch(t))
	var commandError mongo.CommandError
	if receipt != (history.Receipt{}) || !errors.As(err, &commandError) || commandError.Code != 2 {
		t.Fatalf("Append = %+v, %v", receipt, err)
	}
	requireTransaction(t, *commands, "update", "findAndModify", "find", "insert", "abortTransaction")
}

func TestClearCommitsClosureBeforeCleanup(t *testing.T) {
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{NextSequence: 1}), writeOK(), commandOK(), writeOK(), commandOK())
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	if len(*commands) != 6 {
		t.Fatalf("commands = %v", *commands)
	}
	requireTransaction(t, (*commands)[:4], "update", "findAndModify", "update", "commitTransaction")
	if (*commands)[4].name != "delete" || (*commands)[5].name != "commitTransaction" || !(*commands)[4].document.Lookup("startTransaction").Boolean() {
		t.Fatalf("cleanup did not use its own transaction: %v", *commands)
	}
	if (*commands)[4].document.Lookup("txnNumber").Int64() == (*commands)[1].document.Lookup("txnNumber").Int64() {
		t.Fatal("cleanup shares the closure transaction")
	}
	updates := (*commands)[2].document.Lookup("updates").Array().Index(0).Document()
	if !updates.Lookup("u").Document().Lookup(operatorSet).Document().Lookup(fieldClosed).Boolean() {
		t.Fatalf("Clear did not close gate: %v", updates)
	}
	deletion := (*commands)[4].document.Lookup("deletes").Array().Index(0).Document().Lookup("q").Document()
	if deletion.Lookup(fieldKind).StringValue() != kindAppend {
		t.Fatalf("Clear deleted permanent gate: %v", deletion)
	}
}

func TestReadUsesOneSnapshotAndPreservesBatchOrder(t *testing.T) {
	batch := appendBatch(t)
	store, commands := transactionStore(t, gateResponse(conversationGate{}), cursorResponse(appendRecord{Sequence: 1, Messages: string(batch.EncodedMessages())}), commandOK())
	messages, err := store.Read(t.Context(), batch.ConversationID())
	if err != nil || len(messages) != 2 || messages[0].Text() != "first" || messages[1].Text() != "second" {
		t.Fatalf("Read = %v, %v", messages, err)
	}
	if len(*commands) != 3 || (*commands)[0].name != "findAndModify" || (*commands)[1].name != "find" || (*commands)[2].name != "commitTransaction" {
		t.Fatalf("commands = %v", *commands)
	}
	if (*commands)[0].document.Lookup("readConcern").Document().Lookup("level").StringValue() != "snapshot" {
		t.Fatal("Read did not use snapshot")
	}
}

func TestReadClosedAndUnknownConversationsReturnsEmpty(t *testing.T) {
	for _, response := range []bson.D{{{Key: "ok", Value: 1}, {Key: "value", Value: nil}}, gateResponse(conversationGate{Closed: true})} {
		store, _ := transactionStore(t, response, commandOK())
		messages, err := store.Read(t.Context(), "conversation")
		if err != nil || messages == nil || len(messages) != 0 {
			t.Fatalf("Read = %v, %v", messages, err)
		}
	}
}

func TestCleanupFailureCannotRollBackClosure(t *testing.T) {
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{}), writeOK(), commandOK(), bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 2}, {Key: "errmsg", Value: "cleanup failed"}}, commandOK())
	var commandError mongo.CommandError
	if err := store.Clear(t.Context(), "conversation"); !errors.As(err, &commandError) || commandError.Code != 2 {
		t.Fatalf("Clear = %v", err)
	}
	if len(*commands) != 6 {
		t.Fatalf("commands = %v", *commands)
	}
	requireTransaction(t, (*commands)[:4], "update", "findAndModify", "update", "commitTransaction")
	if (*commands)[4].name != "delete" || (*commands)[5].name != "abortTransaction" {
		t.Fatalf("cleanup commands = %v", (*commands)[4:])
	}
}

func TestUnknownCommitResultRetriesOnlyTheCommit(t *testing.T) {
	uncertain := bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 91}, {Key: "errmsg", Value: "lost commit response"}, {Key: "errorLabels", Value: bson.A{"UnknownTransactionCommitResult"}}}
	store, commands := transactionStore(t, writeOK(), gateResponse(conversationGate{}), cursorResponse(), writeOK(), writeOK(), uncertain, commandOK())
	batch := appendBatch(t)
	receipt, err := store.Append(t.Context(), batch)
	if err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("Append = %+v, %v", receipt, err)
	}
	requireTransaction(t, *commands, "update", "findAndModify", "find", "insert", "update", "commitTransaction", "commitTransaction")
}

func TestTransientTransactionConflictRetriesTheSameBatch(t *testing.T) {
	conflict := bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 112}, {Key: "errmsg", Value: "write conflict"}, {Key: "errorLabels", Value: bson.A{"TransientTransactionError"}}}
	store, commands := transactionStore(t, writeOK(), conflict, commandOK(), gateResponse(conversationGate{NextSequence: 5}), cursorResponse(), writeOK(), writeOK(), commandOK())
	batch := appendBatch(t)
	receipt, err := store.Append(t.Context(), batch)
	if err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("Append = %+v, %v", receipt, err)
	}
	if len(*commands) != 8 || (*commands)[1].name != "findAndModify" || (*commands)[2].name != "abortTransaction" || (*commands)[3].name != "findAndModify" {
		t.Fatalf("commands = %v", *commands)
	}
	raw := (*commands)[5].document.Lookup("documents").Array().Index(0).Document()
	var record appendRecord
	if err := bson.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.ID.Data, batchID(batch).Data) || record.Digest != batch.Digest() || record.Messages != string(batch.EncodedMessages()) || record.Sequence != 6 {
		t.Fatalf("retry changed batch identity: %+v", record)
	}
}

func TestListingReleasesEachGateBeforeCheckingTheNext(t *testing.T) {
	store, commands := transactionStore(t, cursorResponse(conversationGroup{ID: "z"}, conversationGroup{ID: "a"}, conversationGroup{ID: "closed"}), commandOK(), gateResponse(conversationGate{}), commandOK(), gateResponse(conversationGate{Closed: true}), commandOK(), gateResponse(conversationGate{}), commandOK())
	ids, err := store.Conversations(t.Context())
	if err != nil || !slices.Equal(ids, []history.ConversationID{"a", "z"}) {
		t.Fatalf("Conversations = %v, %v", ids, err)
	}
	if len(*commands) != 8 {
		t.Fatalf("commands = %v", *commands)
	}
	for index, id := range []history.ConversationID{"a", "closed", "z"} {
		command := (*commands)[2+index*2]
		committed := (*commands)[3+index*2]
		if command.name != "findAndModify" || committed.name != "commitTransaction" || !command.document.Lookup("startTransaction").Boolean() {
			t.Fatalf("gate transaction = %v / %v", command, committed)
		}
		_, key := command.document.Lookup("query").Document().Lookup(fieldID).Binary()
		if !bytes.Equal(key, gateID(id).Data) {
			t.Fatalf("gate order or exact identity changed: %v", command.document)
		}
		if index > 0 && command.document.Lookup("txnNumber").Int64() == (*commands)[index*2].document.Lookup("txnNumber").Int64() {
			t.Fatal("listing held several gates in one transaction")
		}
	}
}

func TestReadRetriesAGateConflictBeforeObservingClosedHistory(t *testing.T) {
	conflict := bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 112}, {Key: "errmsg", Value: "closure changed snapshot"}, {Key: "errorLabels", Value: bson.A{"TransientTransactionError"}}}
	store, commands := transactionStore(t, conflict, commandOK(), gateResponse(conversationGate{Closed: true}), commandOK())
	messages, err := store.Read(t.Context(), "conversation")
	if err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("Read = %v, %v", messages, err)
	}
	if len(*commands) != 4 || (*commands)[0].name != "findAndModify" || (*commands)[1].name != "abortTransaction" || (*commands)[2].name != "findAndModify" || (*commands)[3].name != "commitTransaction" {
		t.Fatalf("stale read was not fenced by gate: %v", *commands)
	}
}
