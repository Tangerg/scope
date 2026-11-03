package runtest

import (
	"context"
	"errors"
	"flag"
	"iter"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

const conformanceProbeEnvironment = "SCOPE_RUNTEST_CONFORMANCE_FAILURE_PROBE"

type conformanceFailure struct {
	name    string
	subtest string
	want    string
	run     func(*testing.T)
}

// A real failed test process proves that the public suites report violations;
// catching an internal verifier error alone cannot establish that contract.
func TestPublicConformanceRejectsBrokenImplementations(t *testing.T) {
	cases := publicConformanceFailures()
	if selected := os.Getenv(conformanceProbeEnvironment); selected != "" {
		for _, test := range cases {
			if test.name == selected {
				test.run(t)
				return
			}
		}
		t.Fatalf("unknown conformance failure probe %q", selected)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.Name()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			pattern := "^" + regexp.QuoteMeta(parent) + "$"
			if test.subtest != "" {
				pattern += "/^" + regexp.QuoteMeta(strings.ReplaceAll(test.subtest, " ", "_")) + "$"
			}
			arguments := []string{"-test.run=" + pattern, "-test.timeout=20s"}
			if coverage := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverage != nil && coverage.Value.String() != "" {
				// Go merges this binary's child counters when the parent test exits.
				arguments = append(arguments, "-test.gocoverdir="+coverage.Value.String())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*conformanceTimeout)
			defer cancel()
			command := exec.CommandContext(ctx, executable, arguments...)
			command.Env = append(os.Environ(), conformanceProbeEnvironment+"="+test.name)
			output, processErr := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(processErr, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("broken implementation did not fail through testing.T: %v\n%s", processErr, output)
			}
			if !strings.Contains(string(output), test.want) {
				t.Fatalf("suite reported the wrong violation; want %q:\n%s", test.want, output)
			}
		})
	}
}

func publicConformanceFailures() []conformanceFailure {
	return []conformanceFailure{
		{"nil store factory", "", "runtest: store factory is nil", func(t *testing.T) { RunStoreConformance(t, nil) }},
		{"acknowledged start disappears", "crash started cut 1", "committed prefix was lost", brokenStoreSuite(omitEveryAppend)},
		{"acknowledged effect intent disappears", "crash effect_intent cut 1", "lost acknowledgment did not leave the exact committed batch", brokenStoreSuite(omitEffectIntent)},
		{"store grants an unauthorized replay policy", "crash effect_intent cut 1", "unacknowledged effect did not receive a durable unknown outcome", brokenStoreSuite(authorizeReplay)},
		{"store rewrites successful settlement", "crash effect_settled cut 1", "recovered outcome=failed", brokenStoreSuite(rewriteSettlement)},
		{"history read drops its first entry", "read yields history after a position", "read after 0", brokenStoreSuite(dropFirstHistoryEntry)},
		{"committed checkpoint disappears", "checkpoint recovery keeps the same local wait", "checkpoint barrier did not commit its recovery baseline", brokenStoreSuite(hideCheckpoint)},
		{"accepted signal receipt disappears", "signal receipts survive recovery", "recovered signal retry accepted=true", brokenStoreSuite(omitSignalReceipt)},
		{"accepted start declaration changes", "start retries do not activate another writer", "conflicts with committed intent", brokenStoreSuite(rewriteStartedInput)},
		{"observation read fails after a step", "local waits have no handshake", "probe cannot read committed steps", brokenStoreSuite(refuseSteppedLoad)},
		{"child admission cannot commit", "one duplicate child key succeeds", "probe cannot commit child admission", brokenStoreSuite(refuseChildAdmission)},
		{"descendant exit cannot commit", "descendant exits precede their parents", "probe cannot commit child exit", brokenStoreSuite(refuseChildExit)},
		{"signal append fails", "local waits have no handshake", "probe cannot admit signal", brokenStoreSuite(refuseSignalReceipt)},
		{"recovery activation fails", "durable observations survive takeover", "probe cannot activate writer", brokenStoreSuite(refuseActivation)},
		{"unknown outcome resolution fails", "canceling uncertainty waits for reconciliation", "probe cannot reconcile outcome", brokenStoreSuite(refuseResolution)},
		{"round trip reports false acknowledgment", "append and load preserve records", "committed log is missing", brokenStoreSuite(omitEveryAppend)},
		{"nil Definition", "", "Definition conformance Definition is nil", func(t *testing.T) { RunDefinitionConformance(t, DefinitionConformanceConfig{}) }},
		{"invalid Descriptor", "descriptor is stable", "invalid Descriptor", invalidDescriptorSuite},
		{"concurrent Descriptor diverges", "descriptor is stable", "concurrent Descriptor results differ", changingDescriptorSuite},
		{"hidden mutable execution input", "fresh executions are isolated and deterministic", "conformance values differ", nondeterministicDefinitionSuite},
		{"restore changes captured state", "restored captured", "restored state", changedRestoredStateSuite},
		{"rejection lacks classification", "rejected declared", "returned an unclassified error", unclassifiedRejectionSuite},
		{"nil resolver", "", "ProgramResolver conformance Resolver is nil", func(t *testing.T) { RunProgramResolverConformance(t, ProgramResolverConformanceConfig{}) }},
		{"resolver returns invalid binding", "exact references resolve to their binding", "resolver returned an invalid Program", invalidResolvedProgramSuite},
		{"resolver falls back by name", "a name collision does not resolve", "by name", nameFallbackSuite},
		{"resolver admits unknown references", "unknown references are rejected", "answered unknown reference", unknownReferenceSuite},
		{"resolver changes interleaved binding", "repeated and interleaved lookups agree", "round 0: runtest: resolver answered", changedResolvedProgramSuite},
		{"concurrent resolver panics", "concurrent lookups agree", "ProgramResolver.Resolve", panickingResolverSuite},
	}
}

func brokenStoreSuite(configure func(*storeProbe, run.Store)) func(*testing.T) {
	return func(t *testing.T) {
		RunStoreConformance(t, func(*testing.T) run.Store {
			store := run.NewMemoryStore()
			probe := &storeProbe{append: store.Append, load: store.Load, read: store.Read}
			configure(probe, store)
			return probe
		})
	}
}

func omitEveryAppend(probe *storeProbe, _ run.Store) {
	probe.append = func(context.Context, run.Batch) error { return nil }
}

func omitEffectIntent(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if _, ok := record.(run.EffectIntent); ok {
				return nil
			}
		}
		return store.Append(ctx, batch)
	}
}

func omitSignalReceipt(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if _, ok := record.(run.SignalAdmitted); ok {
				return nil
			}
		}
		return store.Append(ctx, batch)
	}
}

func authorizeReplay(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		changed, err := batch.Clone()
		if err != nil {
			return err
		}
		for index, record := range changed.Records {
			if intent, ok := record.(run.EffectIntent); ok {
				intent.Policy.Replay = run.ReplayPolicySameIdentity
				changed.Records[index] = intent
			}
		}
		return store.Append(ctx, changed)
	}
}

func rewriteSettlement(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		changed, err := batch.Clone()
		if err != nil {
			return err
		}
		for index, record := range changed.Records {
			if settled, ok := record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusSucceeded {
				failure, err := run.NewFailure(run.FailureKindExternal, "test.outcome.changed", "injected failure")
				if err != nil {
					return err
				}
				outcome, err := run.NewFailedSettlement(failure, settled.Outcome.Payload())
				if err != nil {
					return err
				}
				settled.Outcome = outcome
				changed.Records[index] = settled
			}
		}
		return store.Append(ctx, changed)
	}
}

func dropFirstHistoryEntry(probe *storeProbe, store run.Store) {
	probe.read = func(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
		return func(yield func(run.Entry, error) bool) {
			first := true
			for entry, err := range store.Read(ctx, root, after) {
				if first && err == nil {
					first = false
					continue
				}
				if !yield(entry, err) {
					return
				}
			}
		}
	}
}

// hideCheckpoint serves the whole history from Load, leaving recovery cost
// proportional to the tree's age.
func hideCheckpoint(probe *storeProbe, store run.Store) {
	probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
		entries, err := readStoreHistory(ctx, store, root, 0)
		return run.Log{Root: root, Entries: entries}, len(entries) > 0, err
	}
}

func rewriteStartedInput(probe *storeProbe, store run.Store) {
	probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
		log, found, err := store.Load(ctx, root)
		if found && err == nil {
			started := log.Entries[0].Record.(run.Started)
			started.Input, err = run.EncodePayload("rewritten input")
			log.Entries[0].Record = started
		}
		return log, found, err
	}
}

func refuseSteppedLoad(probe *storeProbe, store run.Store) {
	probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
		log, found, err := store.Load(ctx, root)
		for _, entry := range log.Entries {
			if _, stepped := entry.Record.(run.Stepped); stepped {
				return run.Log{}, false, errors.New("probe cannot read committed steps")
			}
		}
		return log, found, err
	}
}

func refuseChildAdmission(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if started, ok := record.(run.Started); ok && started.Process.Depth() > 0 {
				return errors.New("probe cannot commit child admission")
			}
		}
		return store.Append(ctx, batch)
	}
}

func refuseChildExit(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if exited, ok := record.(run.Exited); ok && exited.Process.Depth() > 0 {
				return errors.New("probe cannot commit child exit")
			}
		}
		return store.Append(ctx, batch)
	}
}

func refuseSignalReceipt(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if _, ok := record.(run.SignalAdmitted); ok {
				return errors.New("probe cannot admit signal")
			}
		}
		return store.Append(ctx, batch)
	}
}

func refuseActivation(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if activation, ok := record.(run.Activated); ok && activation.Epoch > 1 {
				return errors.New("probe cannot activate writer")
			}
		}
		return store.Append(ctx, batch)
	}
}

func refuseResolution(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		for _, record := range batch.Records {
			if settled, ok := record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusSucceeded {
				return errors.New("probe cannot reconcile outcome")
			}
		}
		return store.Append(ctx, batch)
	}
}

func failingDefinitionConfig(t *testing.T) (*definitionConformanceDefinition, DefinitionConformanceConfig) {
	t.Helper()
	definition := newDefinitionConformanceFixture(t)
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return definition, DefinitionConformanceConfig{
		Definition: definition, Input: input, InitialTurn: definitionTurn(1),
		RestoredCases: []ExecutionConformanceCase{{Name: "captured", State: state, Turn: definitionTurn(1)}},
	}
}

func invalidDescriptorSuite(t *testing.T) {
	definition, config := failingDefinitionConfig(t)
	config.Definition = &definitionProbe{describe: func() run.Descriptor { return run.Descriptor{} }, start: definition.Start, restore: definition.Restore}
	RunDefinitionConformance(t, config)
}

func changingDescriptorSuite(t *testing.T) {
	definition, config := failingDefinitionConfig(t)
	other := newRecoveryDefinition(t, recoveryComplete).Descriptor()
	var calls atomic.Uint32
	config.Definition = &definitionProbe{describe: func() run.Descriptor {
		if calls.Add(1) == 1 {
			return definition.Descriptor()
		}
		return other
	}, start: definition.Start, restore: definition.Restore}
	RunDefinitionConformance(t, config)
}

func nondeterministicDefinitionSuite(t *testing.T) {
	definition, config := failingDefinitionConfig(t)
	definition.counter.Store(1)
	RunDefinitionConformance(t, config)
}

func changedRestoredStateSuite(t *testing.T) {
	definition, config := failingDefinitionConfig(t)
	changed, err := run.EncodePayload(definitionConformanceInput{Value: 42})
	if err != nil {
		t.Fatal(err)
	}
	config.Definition = &definitionProbe{describe: definition.Descriptor, start: definition.Start, restore: func(context.Context, run.ExecutionState) (run.Execution, error) {
		return definition.Start(changed)
	}}
	RunDefinitionConformance(t, config)
}

func unclassifiedRejectionSuite(t *testing.T) {
	definition, config := failingDefinitionConfig(t)
	config.RejectedCases = []RejectedStepConformanceCase{{Name: "declared", State: config.RestoredCases[0].State, Turn: definitionTurn(1), FailureKind: run.FailureKindContract, FailureCode: "test.rejected"}}
	config.Definition = &definitionProbe{describe: definition.Descriptor, start: definition.Start, restore: func(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
		execution, err := definition.Restore(ctx, state)
		if err != nil {
			return nil, err
		}
		return &executionProbe{snapshot: execution.Snapshot, step: func(context.Context, run.Turn) (run.Transition, error) {
			return run.Transition{}, errors.New("unclassified rejection")
		}}, nil
	}}
	RunDefinitionConformance(t, config)
}

func invalidResolvedProgramSuite(t *testing.T) {
	program := resolverTestProgram(t, "test.resolver")
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{Resolver: mismatchResolver{}, Resolvable: []run.Program[run.Payload, run.Payload]{program}})
}

func nameFallbackSuite(t *testing.T) {
	program := resolverTestProgram(t, "test.resolver")
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{Resolver: nameFallbackResolver{bindings: map[string]run.Program[run.Payload, run.Payload]{program.ProgramRef().Name(): program}}, Resolvable: []run.Program[run.Payload, run.Payload]{program}})
}

func unknownReferenceSuite(t *testing.T) {
	first, second, _ := resolverConformanceFixture(t)
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{Resolver: mismatchResolver{program: first}, Resolvable: []run.Program[run.Payload, run.Payload]{first}, Unresolvable: []run.ProgramRef{second.ProgramRef()}})
}

func changedResolvedProgramSuite(t *testing.T) {
	first, second, _ := resolverConformanceFixture(t)
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{Resolver: mismatchResolver{program: first}, Resolvable: []run.Program[run.Payload, run.Payload]{first, second}})
}

func panickingResolverSuite(t *testing.T) {
	program := resolverTestProgram(t, "test.resolver")
	RunProgramResolverConformance(t, ProgramResolverConformanceConfig{Resolver: panickingResolver{}, Resolvable: []run.Program[run.Payload, run.Payload]{program}})
}
