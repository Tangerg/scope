//go:build integration

package redis_test

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const redisStartupTimeout = 5 * time.Second

type redisProcess struct {
	address string
	admin   *goredis.Client
}

func newRedisProcess(t *testing.T) *redisProcess {
	t.Helper()
	binary := os.Getenv("SCOPE_REDIS_SERVER")
	if binary == "" {
		binary = "redis-server"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "redis.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	process := exec.Command(binary, "--bind", "127.0.0.1", "--port", port, "--save", "", "--appendonly", "no", "--dir", directory)
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		// The test owns this child and deliberately terminates it without persistence.
		_ = process.Wait()
	})
	admin := goredis.NewClient(&goredis.Options{Addr: address, Protocol: 2, DisableIdentity: true, MaxRetries: -1, DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond})
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(redisStartupTimeout)
	for {
		if err := admin.Ping(t.Context()).Err(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			contents, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("isolated Redis did not start: %s", contents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &redisProcess{address: address, admin: admin}
}

func (r *redisProcess) expireMessages(t *testing.T) {
	t.Helper()
	keys, err := r.admin.Keys(t.Context(), "*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		kind, err := r.admin.Type(t.Context(), key).Result()
		if err != nil {
			t.Fatal(err)
		}
		if kind != "list" {
			continue
		}
		ttl, err := r.admin.PTTL(t.Context(), key).Result()
		if err != nil || ttl <= 0 {
			t.Fatalf("message retention=%v error=%v", ttl, err)
		}
		if err := r.admin.PExpire(t.Context(), key, 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
}
