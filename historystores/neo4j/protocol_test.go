package neo4j_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	neo4jstore "github.com/Tangerg/scope/historystores/neo4j"
)

const (
	boltHello    byte = 0x01
	boltGoodbye  byte = 0x02
	boltReset    byte = 0x0f
	boltRun      byte = 0x10
	boltBegin    byte = 0x11
	boltCommit   byte = 0x12
	boltRollback byte = 0x13
	boltPull     byte = 0x3f
	boltSuccess  byte = 0x70
	boltRecord   byte = 0x71
	boltFailure  byte = 0x7f
)

type boltStep struct {
	contains      []string
	fields        []string
	records       [][]any
	assert        func([]byte)
	commitFailure bool
}

type boltPeer struct{ steps []boltStep }

func (b boltPeer) serve(connection net.Conn) error {
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	handshake := make([]byte, 20)
	if _, err := io.ReadFull(connection, handshake); err != nil {
		return err
	}
	if _, err := connection.Write([]byte{0, 0, 4, 4}); err != nil {
		return err
	}
	step := 0
	for {
		message, err := readBoltMessage(connection)
		if err != nil {
			if errors.Is(err, io.EOF) && step == len(b.steps) {
				return nil
			}
			return err
		}
		if len(message) < 2 {
			return errors.New("missing Bolt message signature")
		}
		switch message[1] {
		case boltHello:
			err = writeBoltMessage(connection, boltSuccess, map[string]any{"server": "Neo4j/5.26.0", "connection_id": "fixture"})
		case boltBegin, boltReset, boltRollback:
			err = writeBoltMessage(connection, boltSuccess, map[string]any{})
		case boltRun:
			if step >= len(b.steps) {
				return errors.New("unexpected extra query")
			}
			query, parseErr := readBoltString(message[2:])
			if parseErr != nil {
				return parseErr
			}
			for _, fragment := range b.steps[step].contains {
				if !strings.Contains(query, fragment) {
					return fmt.Errorf("query %q omits %q", query, fragment)
				}
			}
			if b.steps[step].assert != nil {
				b.steps[step].assert(message)
			}
			err = writeBoltMessage(connection, boltSuccess, map[string]any{"fields": b.steps[step].fields})
		case boltPull:
			if step >= len(b.steps) {
				return errors.New("unexpected extra pull")
			}
			for _, record := range b.steps[step].records {
				if err = writeBoltMessage(connection, boltRecord, record); err != nil {
					return err
				}
			}
			err = writeBoltMessage(connection, boltSuccess, map[string]any{"type": "rw"})
		case boltCommit:
			if step >= len(b.steps) {
				return errors.New("unexpected extra commit")
			}
			if b.steps[step].commitFailure {
				err = writeBoltMessage(connection, boltFailure, map[string]any{"code": "Neo.ClientError.Transaction.TransactionCommitFailed", "message": "acknowledgment unavailable"})
			} else {
				err = writeBoltMessage(connection, boltSuccess, map[string]any{"bookmark": "fixture"})
			}
			step++
		case boltGoodbye:
			if step != len(b.steps) {
				return fmt.Errorf("completed %d/%d queries", step, len(b.steps))
			}
			return nil
		default:
			return fmt.Errorf("unexpected Bolt signature %x", message[1])
		}
		if err != nil {
			return err
		}
	}
}

func readBoltMessage(reader io.Reader) ([]byte, error) {
	var message []byte
	for {
		var header [2]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return nil, err
		}
		size := int(binary.BigEndian.Uint16(header[:]))
		if size == 0 {
			if len(message) == 0 {
				continue
			}
			return message, nil
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return nil, err
		}
		message = append(message, chunk...)
	}
}

func writeBoltMessage(writer io.Writer, signature byte, value any) error {
	message := append([]byte{0xb1, signature}, packBolt(value)...)
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(message)))
	wire := append(header[:], message...)
	_, err := writer.Write(append(wire, 0, 0))
	return err
}

func packBolt(value any) []byte {
	switch value := value.(type) {
	case nil:
		return []byte{0xc0}
	case bool:
		if value {
			return []byte{0xc3}
		}
		return []byte{0xc2}
	case int64:
		out := make([]byte, 9)
		out[0] = 0xcb
		binary.BigEndian.PutUint64(out[1:], uint64(value))
		return out
	case string:
		out := []byte{0xd1, byte(len(value) >> 8), byte(len(value))}
		return append(out, []byte(value)...)
	case []string:
		items := make([]any, len(value))
		for i, v := range value {
			items[i] = v
		}
		return packBolt(items)
	case []any:
		out := []byte{0xd4, byte(len(value))}
		for _, item := range value {
			out = append(out, packBolt(item)...)
		}
		return out
	case map[string]any:
		out := []byte{0xd8, byte(len(value))}
		for key, item := range value {
			out = append(out, packBolt(key)...)
			out = append(out, packBolt(item)...)
		}
		return out
	default:
		panic(fmt.Sprintf("unsupported Bolt fixture value %T", value))
	}
}

func readBoltString(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", io.ErrUnexpectedEOF
	}
	var size, offset int
	switch {
	case raw[0] >= 0x80 && raw[0] <= 0x8f:
		size, offset = int(raw[0]-0x80), 1
	case raw[0] == 0xd0 && len(raw) >= 2:
		size, offset = int(raw[1]), 2
	case raw[0] == 0xd1 && len(raw) >= 3:
		size, offset = int(binary.BigEndian.Uint16(raw[1:3])), 3
	case raw[0] == 0xd2 && len(raw) >= 5:
		size, offset = int(binary.BigEndian.Uint32(raw[1:5])), 5
	default:
		return "", errors.New("unexpected Bolt string encoding")
	}
	if len(raw) < offset+size {
		return "", io.ErrUnexpectedEOF
	}
	return string(raw[offset : offset+size]), nil
}

func boltDriver(t *testing.T, steps ...boltStep) neo4j.Driver {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		done <- (boltPeer{steps: steps}).serve(connection)
	}()
	driver, err := neo4j.NewDriver("bolt://"+listener.Addr().String(), neo4j.NoAuth(), func(c *config.Config) { c.MaxTransactionRetryTime = 0; c.MaxConnectionPoolSize = 1 })
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Close(t.Context()); err != nil {
			t.Error(err)
		}
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Bolt fixture did not stop")
		}
	})
	return driver
}

func schemaStep() boltStep {
	return boltStep{contains: []string{"SHOW NODE UNIQUENESS CONSTRAINTS"}, fields: []string{"labelsOrTypes", "properties"}, records: [][]any{
		{[]any{"ChatHistoryConversation"}, []any{"conversation_id"}},
		{[]any{"ChatHistory"}, []any{"conversation_id", "append_id"}},
	}}
}

func appendBatch(t *testing.T) history.Batch {
	t.Helper()
	batch, err := history.NewBatch("conversation", "append", chat.NewUserMessage(chat.NewTextPart("hello")), chat.NewAssistantMessage(chat.NewTextPart("reply")))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestAppendRequiresCommitAndUsesCanonicalBatch(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		t.Run(fmt.Sprint(failCommit), func(t *testing.T) {
			batch := appendBatch(t)
			step := boltStep{contains: []string{"SET c._scope_lock = true REMOVE c._scope_lock", "NOT c.closed AND existing IS NULL", "a.digest AS digest"}, fields: []string{"closed", "digest"}, records: [][]any{{false, batch.Digest()}}, commitFailure: failCommit, assert: func(wire []byte) {
				if !bytes.Contains(wire, []byte(batch.EncodedMessages())) || !bytes.Contains(wire, []byte(batch.Digest())) {
					t.Error("query did not preserve the canonical Batch")
				}
			}}
			store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: boltDriver(t, schemaStep(), step)})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Append(t.Context(), batch)
			if failCommit {
				if err == nil || receipt != (history.Receipt{}) {
					t.Fatalf("unconfirmed append acknowledged: %+v, %v", receipt, err)
				}
			} else if err != nil || receipt.ValidateFor(batch) != nil {
				t.Fatalf("receipt=%+v error=%v", receipt, err)
			}
		})
	}
}

func TestAppendHonorsClosureAndStoredReceipt(t *testing.T) {
	batch := appendBatch(t)
	for _, test := range []struct {
		name   string
		record []any
		want   error
	}{
		{name: "duplicate", record: []any{false, batch.Digest()}},
		{name: "conflict", record: []any{false, strings.Repeat("0", 64)}, want: history.ErrAppendConflict},
		{name: "closed before digest", record: []any{true, nil}, want: history.ErrConversationClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: boltDriver(t, schemaStep(), boltStep{fields: []string{"closed", "digest"}, records: [][]any{test.record}})})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Append(t.Context(), batch)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.want == nil {
				if receipt.ValidateFor(batch) != nil {
					t.Fatalf("receipt=%+v", receipt)
				}
			} else if receipt != (history.Receipt{}) {
				t.Fatalf("rejected append acknowledged: %+v", receipt)
			}
		})
	}
}

func TestReadAndClearUseTheSameConversationGate(t *testing.T) {
	batch := appendBatch(t)
	read := boltStep{contains: []string{"SET c._scope_lock = true REMOVE c._scope_lock", "WHERE NOT c.closed", "ORDER BY seq"}, fields: []string{"seq", "messages"}, records: [][]any{{int64(1), string(batch.EncodedMessages())}}}
	clear := boltStep{contains: []string{"SET c._scope_lock = true REMOVE c._scope_lock", "SET c.closed = true"}, fields: []string{"closed"}, records: [][]any{{true}}}
	store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: boltDriver(t, schemaStep(), read, clear, boltStep{contains: []string{"WHERE c.closed", "REMOVE a.messages"}, fields: []string{"closed"}, records: [][]any{{true}}})})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Read(t.Context(), "conversation")
	if err != nil || !reflect.DeepEqual(messages, batch.Messages()) {
		t.Fatalf("messages=%v error=%v", messages, err)
	}
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
}

func TestConstructionRejectsMissingUniqueness(t *testing.T) {
	step := schemaStep()
	step.records = step.records[:1]
	store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: boltDriver(t, step)})
	if store != nil || err == nil || !strings.Contains(err.Error(), "uniqueness") {
		t.Fatalf("store=%v error=%v", store, err)
	}
}

func TestCleanupFailureKeepsTheCommittedGateClosed(t *testing.T) {
	batch := appendBatch(t)
	closed := boltStep{contains: []string{"SET c.closed = true"}, fields: []string{"closed"}, records: [][]any{{true}}}
	cleanup := boltStep{contains: []string{"WHERE c.closed", "REMOVE a.messages"}, fields: []string{"closed"}, records: [][]any{{true}}}
	failedCleanup := cleanup
	failedCleanup.commitFailure = true
	appendAfterClose := boltStep{fields: []string{"closed", "digest"}, records: [][]any{{true, nil}}}
	store, err := neo4jstore.NewStore(t.Context(), neo4jstore.StoreConfig{Driver: boltDriver(t, schemaStep(), closed, failedCleanup, appendAfterClose, closed, cleanup)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(t.Context(), "conversation"); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if _, err := store.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("closure was lost: %v", err)
	}
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
}
