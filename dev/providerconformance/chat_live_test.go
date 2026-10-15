//go:build integration

package providerconformance_test

import (
	"os"
	"testing"

	"github.com/Tangerg/scope/dev/providerconformance/internal/chatcheck"
)

func TestChatLive(t *testing.T) {
	selected, err := chatcheck.Select(os.Getenv(chatcheck.LiveSelectionEnv))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range chatcheck.Plan() {
		if selected[entry.ID] && (os.Getenv(entry.KeyEnv) == "" || os.Getenv(entry.ModelEnv) == "") {
			t.Fatalf("selected live binding requires %s and %s", entry.KeyEnv, entry.ModelEnv)
		}
	}
	for _, entry := range chatcheck.Plan() {
		t.Run(entry.ID, func(t *testing.T) {
			for _, scenario := range chatcheck.LiveCases(entry.ID) {
				t.Run(scenario.Semantics, func(t *testing.T) {
					if !selected[entry.ID] {
						t.Skip("unverified: live binding was not explicitly selected")
					}
					runChatScenario(t, entry, scenario.Semantics, os.Getenv(entry.KeyEnv), os.Getenv(entry.ModelEnv))
				})
			}
		})
	}
}
