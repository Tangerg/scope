package run_test

import (
	jsonv2 "encoding/json/v2"
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

	"github.com/Tangerg/scope/agent/run"
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
			invalid: run.ChildWaitBoundaryInvalid,
			valid: map[string]enumValue{
				"terminal_result": run.ChildWaitBoundaryResult,
				"subtree_drained": run.ChildWaitBoundaryDrained,
			},
		},
		"EffectBoundaryKind": {
			invalid: run.EffectBoundaryKindInvalid,
			valid: map[string]enumValue{
				"pending":  run.EffectBoundaryKindPending,
				"settled":  run.EffectBoundaryKindSettled,
				"resolved": run.EffectBoundaryKindResolved,
			},
		},
		"EffectTarget": {
			invalid: run.EffectTargetInvalid,
			valid: map[string]enumValue{
				"framework":  run.EffectTargetFramework,
				"dispatcher": run.EffectTargetDispatcher,
			},
		},
		"EventPhase": {
			invalid: run.EventPhaseInvalid,
			valid: map[string]enumValue{
				"attempt":   run.EventPhaseAttempt,
				"committed": run.EventPhaseCommitted,
			},
		},
		"FailureKind": {
			invalid: run.FailureKindInvalid,
			valid: map[string]enumValue{
				"execution": run.FailureKindExecution,
				"contract":  run.FailureKindContract,
				"external":  run.FailureKindExternal,
				"panic":     run.FailureKindPanic,
			},
		},
		"ProcessInitializationOutcomeStatus": {
			invalid: run.ProcessInitializationOutcomeStatusInvalid,
			valid: map[string]enumValue{
				"initialized": run.ProcessInitializationOutcomeStatusInitialized,
				"failed":      run.ProcessInitializationOutcomeStatusFailed,
			},
		},
		"ProcessWork": {
			invalid: run.ProcessWorkInvalid,
			valid: map[string]enumValue{
				"idle":        run.ProcessWorkIdle,
				"queued":      run.ProcessWorkQueued,
				"step":        run.ProcessWorkStep,
				"restore":     run.ProcessWorkRestore,
				"dispatch":    run.ProcessWorkDispatch,
				"child_start": run.ProcessWorkChildStart,
			},
		},
		"ReplayPolicy": {
			invalid: run.ReplayPolicyInvalid,
			valid: map[string]enumValue{
				"never":         run.ReplayPolicyNever,
				"same_identity": run.ReplayPolicySameIdentity,
			},
		},
		"SettlementStatus": {
			invalid: run.SettlementStatusInvalid,
			valid: map[string]enumValue{
				"succeeded": run.SettlementStatusSucceeded,
				"failed":    run.SettlementStatusFailed,
				"unknown":   run.SettlementStatusUnknown,
			},
		},
		"Status": {
			invalid: run.StatusInvalid,
			valid: map[string]enumValue{
				"running":   run.StatusRunning,
				"waiting":   run.StatusWaiting,
				"paused":    run.StatusPaused,
				"completed": run.StatusCompleted,
				"failed":    run.StatusFailed,
				"canceled":  run.StatusCanceled,
				"timed_out": run.StatusTimedOut,
				"killed":    run.StatusKilled,
			},
		},
		"StepStatus": {
			invalid: run.StepStatusInvalid,
			valid: map[string]enumValue{
				"succeeded": run.StepStatusSucceeded,
				"failed":    run.StepStatusFailed,
				"discarded": run.StepStatusDiscarded,
			},
		},
		"TerminationCause": {
			invalid: run.TerminationCauseInvalid,
			valid: map[string]enumValue{
				"completion":          run.TerminationCauseCompletion,
				"engine_kill":         run.TerminationCauseEngineKill,
				"parent_deadline":     run.TerminationCauseParentDeadline,
				"host_deadline":       run.TerminationCauseHostDeadline,
				"parent_cancellation": run.TerminationCauseParentCancellation,
				"host_cancellation":   run.TerminationCauseHostCancellation,
				"execution_failure":   run.TerminationCauseExecutionFailure,
				"contract_failure":    run.TerminationCauseContractFailure,
				"external_failure":    run.TerminationCauseExternalFailure,
				"panic":               run.TerminationCausePanic,
			},
		},
		"TransitionKind": {
			invalid: run.TransitionKindInvalid,
			valid: map[string]enumValue{
				"continue":   run.TransitionKindContinue,
				"checkpoint": run.TransitionKindCheckpoint,
				"wait":       run.TransitionKindWait,
				"pause":      run.TransitionKindPause,
				"complete":   run.TransitionKindComplete,
				"fail":       run.TransitionKindFail,
			},
		},
		"TreeCheckpointKind": {
			invalid: run.TreeCheckpointKindInvalid,
			valid: map[string]enumValue{
				"start":       run.TreeCheckpointKindStart,
				"child_start": run.TreeCheckpointKindChildStart,
				"signals":     run.TreeCheckpointKindSignals,
				"progress":    run.TreeCheckpointKindProgress,
				"parked":      run.TreeCheckpointKindParked,
				"terminal":    run.TreeCheckpointKindTerminal,
			},
		},
		"TreeFreezePhase": {
			invalid: run.TreeFreezePhaseInvalid,
			valid: map[string]enumValue{
				"none":      run.TreeFreezePhaseNone,
				"acquiring": run.TreeFreezePhaseAcquiring,
				"held":      run.TreeFreezePhaseHeld,
			},
		},
		"WaitKind": {
			invalid: run.WaitKindInvalid,
			valid: map[string]enumValue{
				"external": run.WaitKindExternal,
				"children": run.WaitKindChildren,
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
					data, err := jsonv2.Marshal(value)
					if err != nil || string(data) != strconv.Quote(want) {
						t.Fatalf("JSON = %s, error = %v; want %q", data, err, want)
					}
					decoded := reflect.New(typ)
					if err := jsonv2.Unmarshal(data, decoded.Interface()); err != nil {
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
	pkg, err := compiler.Import(reflect.TypeFor[run.Status]().PkgPath())
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
	capability, err := run.ParseCapability("scope.tool.invoke")
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
	var decoded run.Capability
	if err := decoded.UnmarshalText(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded != capability {
		t.Fatalf("capability round trip = %q, want %q", decoded, capability)
	}

	var zero run.Capability
	if zero.Valid() {
		t.Error("the zero Capability reports itself valid")
	}
	if _, err := zero.MarshalText(); err == nil {
		t.Error("the zero Capability encoded without error")
	}
	if err := (*run.Capability)(nil).UnmarshalText([]byte("scope.tool.invoke")); err == nil {
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
			if _, err := run.ParseCapability(invalid); err == nil {
				t.Fatalf("ParseCapability(%q) succeeded", invalid)
			}
			if err := decoded.UnmarshalText([]byte(invalid)); err == nil {
				t.Fatalf("UnmarshalText(%q) succeeded", invalid)
			}
		})
	}
}
