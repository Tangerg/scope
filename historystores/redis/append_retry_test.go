package redis_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/redis"
)

type historyPeer struct {
	mu       sync.Mutex
	appends  int
	messages []string
	workers  sync.WaitGroup
}

func (h *historyPeer) dial(context.Context, string, string) (net.Conn, error) {
	client, server := net.Pipe()
	h.workers.Go(func() { defer server.Close(); h.serve(server) })
	return client, nil
}

func (h *historyPeer) serve(connection net.Conn) {
	reader := bufio.NewReader(connection)
	var queued [][]string
	transaction := false
	for {
		args, err := readHistoryCommand(reader)
		if err != nil {
			return
		}
		command := strings.ToLower(args[0])
		if command == "hello" {
			if _, err := io.WriteString(connection, "-ERR unknown command 'hello'\r\n"); err != nil {
				return
			}
			continue
		}
		if command == "multi" {
			transaction = true
			_, _ = io.WriteString(connection, "+OK\r\n")
			continue
		}
		if transaction && command != "exec" {
			queued = append(queued, args)
			_, _ = io.WriteString(connection, "+QUEUED\r\n")
			continue
		}
		h.mu.Lock()
		switch command {
		case "rpush":
			h.appends++
			h.messages = append(h.messages, args[2:]...)
			// The first append executes but loses its reply.
			if h.appends == 1 {
				h.mu.Unlock()
				return
			}
			_, _ = fmt.Fprintf(connection, ":%d\r\n", len(h.messages))
		case "exec":
			for _, item := range queued {
				if strings.EqualFold(item[0], "rpush") {
					h.appends++
					h.messages = append(h.messages, item[2:]...)
				}
			}
			if h.appends == 1 {
				h.mu.Unlock()
				return
			}
			_, _ = io.WriteString(connection, "*2\r\n:2\r\n:1\r\n")
			transaction = false
			queued = nil
		case "lrange":
			_, _ = fmt.Fprintf(connection, "*%d\r\n", len(h.messages))
			for _, message := range h.messages {
				_, _ = fmt.Fprintf(connection, "$%d\r\n%s\r\n", len(message), message)
			}
		default:
			_, _ = io.WriteString(connection, "+OK\r\n")
		}
		h.mu.Unlock()
	}
}

func readHistoryCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, count)
	for index := range count {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
		if err != nil {
			return nil, err
		}
		value := make([]byte, size+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		args[index] = string(value[:size])
	}
	return args, nil
}

func TestLostAppendReplyNeverRepeatsHistoryWrite(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Minute} {
		t.Run(ttl.String(), func(t *testing.T) {
			peer := &historyPeer{}
			client := goredis.NewClient(&goredis.Options{Dialer: peer.dial, Protocol: 2, DisableIdentity: true})
			t.Cleanup(func() { _ = client.Close(); peer.workers.Wait() })
			store, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: client, ScanNodes: noEnumeration, TTL: ttl})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := store.Write(t.Context(), "conversation", chat.NewUserMessage(chat.NewTextPart("once")))
			if outcome != (history.WriteOutcome{Uncertain: true}) || err == nil || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe)) {
				t.Fatalf("lost reply outcome=%+v err=%v", outcome, err)
			}
			messages, err := store.Read(t.Context(), "conversation")
			if err != nil || len(messages) != 1 || messages[0].Text() != "once" {
				t.Fatalf("physical history=%v err=%v", messages, err)
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if peer.appends != 1 {
				t.Fatalf("one Write caused %d appends", peer.appends)
			}
		})
	}
}
