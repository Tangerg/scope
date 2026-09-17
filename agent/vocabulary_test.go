package agent_test

import (
	"encoding/json"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
)

type enumValue interface {
	Valid() bool
	String() string
}

type vocabulary struct {
	invalid enumValue
	valid   map[string]enumValue
}

// Exact literals protect persisted vocabulary; source discovery makes missing
// types and members fail here when the public enum declarations change.
func TestStableEnumVocabulary(t *testing.T) {
	vocabularies := map[string]vocabulary{
		"ChildWaitBoundary": {
			invalid: agent.ChildWaitBoundaryInvalid,
			valid: map[string]enumValue{
				"terminal_result": agent.ChildWaitBoundaryResult,
				"subtree_drained": agent.ChildWaitBoundaryDrained,
			},
		},
		"EffectBoundaryKind": {
			invalid: agent.EffectBoundaryKindInvalid,
			valid: map[string]enumValue{
				"pending":  agent.EffectBoundaryKindPending,
				"settled":  agent.EffectBoundaryKindSettled,
				"resolved": agent.EffectBoundaryKindResolved,
			},
		},
		"EffectTarget": {
			invalid: agent.EffectTargetInvalid,
			valid: map[string]enumValue{
				"framework":  agent.EffectTargetFramework,
				"dispatcher": agent.EffectTargetDispatcher,
			},
		},
		"EventPhase": {
			invalid: agent.EventPhaseInvalid,
			valid: map[string]enumValue{
				"attempt":   agent.EventPhaseAttempt,
				"committed": agent.EventPhaseCommitted,
			},
		},
		"FailureKind": {
			invalid: agent.FailureKindInvalid,
			valid: map[string]enumValue{
				"execution": agent.FailureKindExecution,
				"contract":  agent.FailureKindContract,
				"external":  agent.FailureKindExternal,
				"panic":     agent.FailureKindPanic,
			},
		},
		"ProcessInitializationOutcomeStatus": {
			invalid: agent.ProcessInitializationOutcomeStatusInvalid,
			valid: map[string]enumValue{
				"initialized": agent.ProcessInitializationOutcomeStatusInitialized,
				"failed":      agent.ProcessInitializationOutcomeStatusFailed,
			},
		},
		"ProcessWork": {
			invalid: agent.ProcessWorkInvalid,
			valid: map[string]enumValue{
				"idle":        agent.ProcessWorkIdle,
				"queued":      agent.ProcessWorkQueued,
				"step":        agent.ProcessWorkStep,
				"restore":     agent.ProcessWorkRestore,
				"dispatch":    agent.ProcessWorkDispatch,
				"child_start": agent.ProcessWorkChildStart,
			},
		},
		"ReplayPolicy": {
			invalid: agent.ReplayPolicyInvalid,
			valid: map[string]enumValue{
				"never":         agent.ReplayPolicyNever,
				"same_identity": agent.ReplayPolicySameIdentity,
			},
		},
		"SettlementStatus": {
			invalid: agent.SettlementStatusInvalid,
			valid: map[string]enumValue{
				"succeeded": agent.SettlementStatusSucceeded,
				"failed":    agent.SettlementStatusFailed,
				"unknown":   agent.SettlementStatusUnknown,
			},
		},
		"Status": {
			invalid: agent.StatusInvalid,
			valid: map[string]enumValue{
				"running":   agent.StatusRunning,
				"waiting":   agent.StatusWaiting,
				"paused":    agent.StatusPaused,
				"completed": agent.StatusCompleted,
				"failed":    agent.StatusFailed,
				"canceled":  agent.StatusCanceled,
				"timed_out": agent.StatusTimedOut,
				"killed":    agent.StatusKilled,
			},
		},
		"StepStatus": {
			invalid: agent.StepStatusInvalid,
			valid: map[string]enumValue{
				"succeeded": agent.StepStatusSucceeded,
				"failed":    agent.StepStatusFailed,
				"discarded": agent.StepStatusDiscarded,
			},
		},
		"TerminationCause": {
			invalid: agent.TerminationCauseInvalid,
			valid: map[string]enumValue{
				"completion":          agent.TerminationCauseCompletion,
				"engine_kill":         agent.TerminationCauseEngineKill,
				"process_deadline":    agent.TerminationCauseProcessDeadline,
				"parent_deadline":     agent.TerminationCauseParentDeadline,
				"host_deadline":       agent.TerminationCauseHostDeadline,
				"parent_cancellation": agent.TerminationCauseParentCancellation,
				"host_cancellation":   agent.TerminationCauseHostCancellation,
				"execution_failure":   agent.TerminationCauseExecutionFailure,
				"contract_failure":    agent.TerminationCauseContractFailure,
				"external_failure":    agent.TerminationCauseExternalFailure,
				"panic":               agent.TerminationCausePanic,
			},
		},
		"TransitionKind": {
			invalid: agent.TransitionKindInvalid,
			valid: map[string]enumValue{
				"continue":   agent.TransitionKindContinue,
				"checkpoint": agent.TransitionKindCheckpoint,
				"wait":       agent.TransitionKindWait,
				"pause":      agent.TransitionKindPause,
				"complete":   agent.TransitionKindComplete,
				"fail":       agent.TransitionKindFail,
			},
		},
		"TreeCheckpointKind": {
			invalid: agent.TreeCheckpointKindInvalid,
			valid: map[string]enumValue{
				"start":       agent.TreeCheckpointKindStart,
				"child_start": agent.TreeCheckpointKindChildStart,
				"signals":     agent.TreeCheckpointKindSignals,
				"progress":    agent.TreeCheckpointKindProgress,
				"parked":      agent.TreeCheckpointKindParked,
				"terminal":    agent.TreeCheckpointKindTerminal,
			},
		},
		"TreeFreezePhase": {
			invalid: agent.TreeFreezePhaseInvalid,
			valid: map[string]enumValue{
				"none":      agent.TreeFreezePhaseNone,
				"acquiring": agent.TreeFreezePhaseAcquiring,
				"held":      agent.TreeFreezePhaseHeld,
			},
		},
		"WaitKind": {
			invalid: agent.WaitKindInvalid,
			valid: map[string]enumValue{
				"external": agent.WaitKindExternal,
				"children": agent.WaitKindChildren,
			},
		},
	}
	assertVocabularyCoverage(t, vocabularies)
	for name, enum := range vocabularies {
		t.Run(name, func(t *testing.T) {
			typ := reflect.TypeOf(enum.invalid)
			if typ.Name() != name || !reflect.ValueOf(enum.invalid).IsZero() {
				t.Fatalf("invalid value = %T(%v), want zero %s", enum.invalid, enum.invalid, name)
			}
			if enum.invalid.Valid() || enum.invalid.String() != "invalid" {
				t.Fatalf("zero value = %q, valid = %t", enum.invalid.String(), enum.invalid.Valid())
			}
			for want, value := range enum.valid {
				t.Run(want, func(t *testing.T) {
					if reflect.TypeOf(value) != typ || !value.Valid() || value.String() != want {
						t.Fatalf("value = %T(%q), valid = %t; want %s(%q)", value, value.String(), value.Valid(), name, want)
					}
					data, err := json.Marshal(value)
					if err != nil || string(data) != strconv.Quote(want) {
						t.Fatalf("JSON = %s, error = %v; want %q", data, err, want)
					}
					decoded := reflect.New(typ)
					if err := json.Unmarshal(data, decoded.Interface()); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(decoded.Elem().Interface(), value) {
						t.Fatalf("JSON round trip = %v, want %v", decoded.Elem(), value)
					}
				})
			}
		})
	}
}

func assertVocabularyCoverage(t *testing.T, vocabularies map[string]vocabulary) {
	t.Helper()
	// Compiler export data includes inferred constant types and declarations
	// across files; a syntax-only inventory can miss both.
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", "-export", "-f", "{{.ImportPath}}\t{{.Export}}", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("load enum declarations: %v\n%s", err, output)
	}
	exports := make(map[string]string)
	for line := range strings.Lines(string(output)) {
		path, archive, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok {
			exports[path] = archive
		}
	}
	compiler := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})
	pkg, err := compiler.Import(reflect.TypeFor[agent.Status]().PkgPath())
	if err != nil {
		t.Fatal(err)
	}
	members := make(map[string]int)
	for _, name := range pkg.Scope().Names() {
		constant, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok || !constant.Exported() {
			continue
		}
		typ, ok := constant.Type().(*types.Named)
		if !ok || !typ.Obj().Exported() || typ.Obj().Pkg() != pkg {
			continue
		}
		methods := types.NewMethodSet(types.NewPointer(typ))
		if methods.Lookup(pkg, "Valid") != nil && methods.Lookup(pkg, "String") != nil {
			members[typ.Obj().Name()]++
		}
	}
	for name, count := range members {
		enum, covered := vocabularies[name]
		if !covered {
			t.Errorf("exported enum %s has no pinned vocabulary", name)
		} else if got := len(enum.valid) + 1; got != count {
			t.Errorf("%s pins %d members including zero; source declares %d", name, got, count)
		}
	}
	for name := range vocabularies {
		if members[name] == 0 {
			t.Errorf("stale vocabulary for %s", name)
		}
	}
}

// TestCapabilityIsAQualifiedName keeps capability names inside the one shape
// attenuation is defined over. A capability that round-trips through text
// differently than it parsed would silently widen or narrow authority.
func TestCapabilityIsAQualifiedName(t *testing.T) {
	capability, err := agent.ParseCapability("scope.tool.invoke")
	if err != nil {
		t.Fatal(err)
	}
	if !capability.Valid() || capability.String() != "scope.tool.invoke" {
		t.Fatalf("capability = %q, valid = %t", capability, capability.Valid())
	}

	encoded, err := capability.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var decoded agent.Capability
	if err := decoded.UnmarshalText(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded != capability {
		t.Fatalf("capability round trip = %q, want %q", decoded, capability)
	}

	var zero agent.Capability
	if zero.Valid() {
		t.Error("the zero Capability reports itself valid")
	}
	if _, err := zero.MarshalText(); err == nil {
		t.Error("the zero Capability encoded without error")
	}
	if err := (*agent.Capability)(nil).UnmarshalText([]byte("scope.tool.invoke")); err == nil {
		t.Error("a nil Capability receiver accepted text")
	}
	for name, invalid := range map[string]string{
		"empty":           "",
		"uppercase":       "Scope.Tool",
		"whitespace":      "scope tool",
		"leading digit":   "1scope.tool",
		"leading dot":     ".scope.tool",
		"slash separated": "scope/tool",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agent.ParseCapability(invalid); err == nil {
				t.Fatalf("ParseCapability(%q) succeeded", invalid)
			}
			if err := decoded.UnmarshalText([]byte(invalid)); err == nil {
				t.Fatalf("UnmarshalText(%q) succeeded", invalid)
			}
		})
	}
}
