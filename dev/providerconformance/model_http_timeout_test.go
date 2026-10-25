package providerconformance_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/models/protocol/anthropic"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestProtocolHTTPDefaultsPreserveHeaderDeadline(t *testing.T) {
	for _, provider := range []struct {
		name string
		call func(context.Context, *http.Client) error
	}{
		{name: "openai", call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: "http://scope.test", HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.ListModels(ctx, 1, 1024)
			return err
		}},
		{name: "anthropic", call: func(ctx context.Context, client *http.Client) error {
			model, err := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: "test-key", BaseURL: "http://scope.test", HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.ListModels(ctx, 1, 1024)
			return err
		}},
	} {
		for _, scenario := range []struct {
			name       string
			delayBody  bool
			hostClient bool
			elapsed    time.Duration
		}{
			{name: "default header deadline", elapsed: 10 * time.Minute},
			{name: "streaming body has no default deadline", delayBody: true, elapsed: 11 * time.Minute},
			{name: "host header deadline survives", hostClient: true, elapsed: 3 * time.Minute},
		} {
			t.Run(provider.name+"/"+scenario.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
					defer cancel()
					transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
						client, server := net.Pipe()
						go serveModelListResponse(ctx, t, server, scenario.delayBody)
						return client, nil
					}}
					defer transport.CloseIdleConnections()
					original := http.DefaultTransport
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = original }()
					var client *http.Client
					if scenario.hostClient {
						transport.ResponseHeaderTimeout = scenario.elapsed
						client = &http.Client{Transport: transport}
					}
					started := time.Now()
					err := provider.call(ctx, client)
					if elapsed := time.Since(started); elapsed != scenario.elapsed {
						t.Fatalf("request lasted %s; want %s: %v", elapsed, scenario.elapsed, err)
					}
					if !scenario.hostClient && transport.ResponseHeaderTimeout != 0 {
						t.Fatal("default transport was mutated")
					}
					if scenario.delayBody {
						if err != nil {
							t.Fatalf("body was cut off after headers: %v", err)
						}
						return
					}
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
						t.Fatalf("expected transport header timeout before context deadline: %v", err)
					}
				})
			})
		}
	}
}

func serveModelListResponse(ctx context.Context, t *testing.T, connection net.Conn, delayBody bool) {
	t.Helper()
	defer connection.Close()
	request, err := http.ReadRequest(bufio.NewReader(connection))
	if err != nil {
		t.Errorf("read request: %v", err)
		return
	}
	defer request.Body.Close()
	if !delayBody {
		<-ctx.Done()
		return
	}
	const body = `{"data":[],"has_more":false}`
	if _, err := fmt.Fprintf(connection, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(body)); err != nil {
		t.Errorf("write headers: %v", err)
		return
	}
	time.Sleep(11 * time.Minute)
	if _, err := io.WriteString(connection, body); err != nil {
		t.Errorf("write body: %v", err)
	}
}
