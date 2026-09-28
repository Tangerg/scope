package modeltest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// WithTimeout derives the call deadline from the test context.
func WithTimeout(t *testing.T, duration time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), duration)
}

const envKeyPrefix = "SCOPE_TEST_"

// RequireKey reads SCOPE_TEST_<PROVIDER>_KEY and skips the test when empty.
func RequireKey(t *testing.T, provider string) string {
	t.Helper()
	name := envKeyPrefix + strings.ToUpper(provider) + "_KEY"
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("set %s to run this integration test", name)
	}
	return v
}

// RequireEnv skips the test when the named environment variable is empty.
func RequireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("set %s to run this integration test", name)
	}
	return v
}

// LookupEnv treats an empty value as absent.
func LookupEnv(name string) (string, bool) {
	v := os.Getenv(name)
	return v, v != ""
}
