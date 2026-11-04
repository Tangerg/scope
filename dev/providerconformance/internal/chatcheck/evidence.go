package chatcheck

import (
	"bufio"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"strings"
)

type TestAction string

const (
	ActionRun    TestAction = "run"
	ActionPass   TestAction = "pass"
	ActionFail   TestAction = "fail"
	ActionSkip   TestAction = "skip"
	ActionNotRun TestAction = "not_run"
)

type EvidenceLevel string

const (
	LevelOfflineProtocol EvidenceLevel = "offline_protocol"
	LevelOfflineBinding  EvidenceLevel = "offline_binding"
	LevelLiveModel       EvidenceLevel = "live_model"
)

type EvidenceStatus string

const (
	StatusUnverified EvidenceStatus = "unverified"
	StatusVerified   EvidenceStatus = "verified"
	StatusFailed     EvidenceStatus = "failed"
)

const (
	initialEventBuffer = 4 << 10
	maxEventBytes      = 4 << 20
)

type Event struct {
	Action TestAction
	Test   string
}

type Evidence struct {
	Entry string        `json:"entry"`
	Level EvidenceLevel `json:"level"`
	Model string        `json:"model,omitempty"`
	Case
	Status    EvidenceStatus `json:"status"`
	Outcome   TestAction     `json:"outcome"`
	Directory string         `json:"directory"`
}

// Results retains test outcomes only. Provider output can contain credentials
// or prompts and must not enter the evidence artifact.
type Results struct {
	actions map[string]TestAction
}

func Read(reader io.Reader) (Results, error) {
	results := Results{actions: make(map[string]TestAction)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, initialEventBuffer), maxEventBytes)
	for scanner.Scan() {
		var event Event
		if err := jsonv2.Unmarshal(scanner.Bytes(), &event); err != nil {
			return results, fmt.Errorf("decode go test event: %w", err)
		}
		if event.Test == "" {
			continue
		}
		switch event.Action {
		case ActionRun, ActionPass, ActionFail, ActionSkip:
			results.actions[event.Test] = event.Action
		}
	}
	return results, scanner.Err()
}

func (r Results) Evidence(entry string, level EvidenceLevel, model string, scenario Case) Evidence {
	evidence := Evidence{Entry: entry, Level: level, Model: model, Case: scenario, Status: StatusUnverified, Outcome: r.actions[scenario.Test]}
	if evidence.Outcome == "" {
		evidence.Outcome = ActionNotRun
	}
	if r.actions[scenario.Test] == ActionPass {
		evidence.Status = StatusVerified
	}
	for name, action := range r.actions {
		if name != scenario.Test && !strings.HasPrefix(name, scenario.Test+"/") {
			continue
		}
		if action == ActionFail {
			evidence.Status = StatusFailed
			break
		}
		if action != ActionPass {
			evidence.Status = StatusUnverified
		}
	}
	if evidence.Status != StatusVerified {
		evidence.Support = SupportUnknown
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
