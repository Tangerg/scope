package replicate

import (
	"strings"
	"testing"
)

func TestDownloadOutputRejectsUntrustedHost(t *testing.T) {
	client, err := newAPI(t.Context(), apiConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = client.downloadOutput(t.Context(), "https://example.com/output.png")
	if err == nil || !strings.Contains(err.Error(), `untrusted output host "example.com"`) {
		t.Fatalf("downloadOutput error = %v", err)
	}
}
