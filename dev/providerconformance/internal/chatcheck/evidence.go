package chatcheck

import (
	"bufio"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"strings"
)

type Event struct {
	Action string
	Test   string
}

type Evidence struct {
	Entry string `json:"entry"`
	Level string `json:"level"`
	Model string `json:"model,omitempty"`
	Case
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	Directory string `json:"directory"`
}

// Results retains test outcomes only. Provider output can contain credentials
// or prompts and must not enter the evidence artifact.
type Results struct {
	actions map[string]string
}

func Read(reader io.Reader) (Results, error) {
	results := Results{actions: make(map[string]string)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var event Event
		if err := jsonv2.Unmarshal(scanner.Bytes(), &event); err != nil {
			return results, fmt.Errorf("decode go test event: %w", err)
		}
		if event.Test == "" {
			continue
		}
		switch event.Action {
		case "run", "pass", "fail", "skip":
			results.actions[event.Test] = event.Action
		}
	}
	return results, scanner.Err()
}

func (r Results) Evidence(entry, level, model string, scenario Case) Evidence {
	evidence := Evidence{Entry: entry, Level: level, Model: model, Case: scenario, Status: "unverified", Outcome: r.actions[scenario.Test]}
	if evidence.Outcome == "" {
		evidence.Outcome = "not_run"
	}
	if r.actions[scenario.Test] == "pass" {
		evidence.Status = "verified"
	}
	for name, action := range r.actions {
		if name != scenario.Test && !strings.HasPrefix(name, scenario.Test+"/") {
			continue
		}
		if action == "fail" {
			evidence.Status = "failed"
			break
		}
		if action != "pass" {
			evidence.Status = "unverified"
		}
	}
	if evidence.Status != "verified" {
		evidence.Support = "unknown"
	}
	return evidence
}

func Select(value string) (map[string]bool, error) {
	selected := make(map[string]bool)
	if value == "" {
		return selected, nil
	}
	for _, id := range strings.Split(value, ",") {
		known := false
		for _, entry := range Plan() {
			known = known || entry.ID == id
		}
		if !known || selected[id] {
			return nil, fmt.Errorf("unknown or repeated chat entry %q", id)
		}
		selected[id] = true
	}
	return selected, nil
}
