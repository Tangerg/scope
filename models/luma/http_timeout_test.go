package luma_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/luma"
)

func TestDefaultClientBoundsResponseHeadersWithoutTimingOutBody(t *testing.T) {
	for _, stallHeaders := range []bool{true, false} {
		name := "slow body"
		if stallHeaders {
			name = "stalled headers"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					client, server := net.Pipe()
					go func() {
						defer server.Close()
						request, err := http.ReadRequest(bufio.NewReader(server))
						if err != nil {
							t.Error(err)
							return
						}
						_, _ = io.Copy(io.Discard, request.Body)
						_ = request.Body.Close()
						if stallHeaders {
							_, _ = io.Copy(io.Discard, server)
							return
						}
						const body = `{"error":"test complete"}`
						_, _ = fmt.Fprintf(server, "HTTP/1.1 400 Bad Request\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(body))
						time.Sleep(11 * time.Minute)
						_, _ = io.WriteString(server, body)
					}()
					return client, nil
				}}
				previous := http.DefaultTransport
				http.DefaultTransport = transport
				defer func() { http.DefaultTransport = previous }()
				defer transport.CloseIdleConnections()

				model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
					APIKey: "test", BaseURL: "http://api.example/v1",
					DefaultOptions: image.Options{Model: luma.ModelUni1},
				})
				if err != nil {
					t.Fatal(err)
				}
				request, err := image.NewRequest("a tree")
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
				defer cancel()
				start := time.Now()
				_, err = model.Call(ctx, request)
				elapsed := time.Since(start)
				if stallHeaders {
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
						t.Fatalf("stalled response error = %v, elapsed = %s", err, elapsed)
					}
					if elapsed < 30*time.Minute || elapsed >= 31*time.Minute {
						t.Fatalf("three header waits elapsed = %s, want 30 minutes plus retry delays", elapsed)
					}
				} else if elapsed != 11*time.Minute || err == nil || !strings.Contains(err.Error(), "test complete") {
					t.Fatalf("slow body error = %v, elapsed = %s", err, elapsed)
				}
				if http.DefaultTransport != transport || transport.ResponseHeaderTimeout != 0 {
					t.Fatal("default transport policy was mutated")
				}
			})
		})
	}
}
