//go:build unix

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestMCPProcessBoundary(t *testing.T) {
	binary := buildExpert(t)
	t.Run("authorized result and refusal", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		peer := openExpert(t, ctx, binary)
		prompt, err := fetchSystemPrompt(ctx, peer.session, briefingTopic)
		if err != nil || !strings.Contains(prompt, "Independent research") {
			t.Fatalf("external prompt=%q error=%v", prompt, err)
		}
		for _, permit := range []bool{false, true} {
			runtime := managedRuntime(t, agentrun.NewMemoryStore())
			program := peerProgram(t, ctx, runtime.Compiler(), peer, permit, briefingQuery)
			brief, err := runBriefing(ctx, runtime, program, prompt, briefingTopic)
			if err != nil {
				t.Fatal(err)
			}
			if permit {
				peer.await(t, ctx, expertStarted)
				if !slices.Equal(brief.Sources, []string{expertSourceURL}) || brief.ToolError != "" || peer.status(t, ctx).Calls != 1 {
					t.Fatalf("authorized brief=%+v", brief)
				}
			} else if len(brief.Sources) != 0 || !strings.Contains(brief.ToolError, "not authorized") || peer.status(t, ctx).Calls != 0 {
				t.Fatalf("refused brief=%+v", brief)
			}
		}
	})
	t.Run("cancellation reaches the peer without closing its session", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		peer := openExpert(t, ctx, binary)
		store := agentrun.NewMemoryStore()
		runtime := managedRuntime(t, store)
		program := peerProgram(t, ctx, runtime.Compiler(), peer, true, expertWaitQuery)
		root := startManagedBriefing(t, ctx, runtime, program)
		peer.await(t, ctx, expertStarted)
		if err := root.Cancel(ctx, "stop the external search"); err != nil {
			t.Fatal(err)
		}
		peer.await(t, ctx, expertCanceled)
		awaitUnknown(t, ctx, root)
		if status := peer.status(t, ctx); status.Calls != 1 || status.Canceled != 1 {
			t.Fatalf("peer cancellation=%+v", status)
		}
		log := loadLog(t, ctx, store, root.ID())
		unknownRequest(t, log)
		assertUnresolved(t, log)
	})
	t.Run("reconnection cannot replay or settle an uncertain call", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		peer := openExpert(t, ctx, binary)
		store := agentrun.NewMemoryStore()
		runtime := managedRuntime(t, store)
		program := peerProgram(t, ctx, runtime.Compiler(), peer, true, expertDisconnectQuery)
		root := startManagedBriefing(t, ctx, runtime, program)
		peer.await(t, ctx, expertStarted)
		awaitUnknown(t, ctx, root)
		if result, err := peer.command.Wait(); err != nil || result.ExitCode != expertExitCode {
			t.Fatalf("expert exit=%+v error=%v", result, err)
		}
		if err := runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
		before := loadLog(t, ctx, store, root.ID())
		original := unknownRequest(t, before)
		reconnected := openExpert(t, ctx, binary)
		replacement := managedRuntime(t, store)
		changedGrant := peerProgram(t, ctx, replacement.Compiler(), reconnected, false, expertDisconnectQuery)
		if changedGrant.ProgramRef() == program.ProgramRef() {
			t.Fatal("authorization change did not change the exact binding")
		}
		if _, err := replacement.Recover(ctx, changedGrant, before.Root); !errors.Is(err, agentrun.ErrInvalidProgram) {
			t.Fatalf("restoration with changed authorization=%v", err)
		}
		if loaded := loadLog(t, ctx, store, before.Root); loaded.Head() != before.Head() || loaded.Epoch() != before.Epoch() {
			t.Fatal("rejected restoration advanced the authoritative log")
		}
		exact := peerProgram(t, ctx, replacement.Compiler(), reconnected, true, expertDisconnectQuery)
		if exact.ProgramRef() != program.ProgramRef() {
			t.Fatal("session identity leaked into the program binding")
		}
		restored, err := replacement.Recover(ctx, exact, before.Root)
		if err != nil {
			t.Fatal(err)
		}
		if current := unknownRequest(t, loadLog(t, ctx, store, before.Root)); current.ID() != original.ID() {
			t.Fatal("reconnection changed the uncertain Effect identity")
		}
		if status := reconnected.status(t, ctx); status.Calls != 0 {
			t.Fatalf("recovery replayed an uncertain call: %+v", status)
		}
		if cancelErr := restored.Cancel(ctx, "leave the remote outcome unresolved"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		unresolved := loadLog(t, ctx, store, before.Root)
		assertUnresolved(t, unresolved)
		if unknownRequest(t, unresolved).ID() != original.ID() {
			t.Fatal("cancellation erased uncertainty")
		}
		fresh := peerProgram(t, ctx, replacement.Compiler(), reconnected, true, briefingQuery)
		brief, err := runBriefing(ctx, replacement, fresh, "Use the external expert.", briefingTopic)
		if err != nil || !slices.Equal(brief.Sources, []string{expertSourceURL}) {
			t.Fatalf("fresh task after reconnect=%+v error=%v", brief, err)
		}
		reconnected.await(t, ctx, expertStarted)
		if status := reconnected.status(t, ctx); status.Calls != 1 {
			t.Fatalf("fresh task calls=%+v", status)
		}
		if loadLog(t, ctx, store, before.Root).Head() != unresolved.Head() {
			t.Fatal("a fresh task advanced the unresolved execution")
		}
	})
}

func peerProgram(t *testing.T, ctx context.Context, compiler *agentrun.Compiler, peer *stdioPeer, permit bool, query string) agentrun.Program[interaction.Input, interaction.Output] {
	t.Helper()
	program, err := newBriefingProgram(ctx, compiler, briefingConfig{Session: peer.session, ExpertRevision: peer.artifact.digest.String(), AllowSearch: permit, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func managedRuntime(t *testing.T, store *agentrun.MemoryStore) *agentrun.Runtime {
	t.Helper()
	runtime, err := agentrun.New(t.Context(), agentrun.Config{Store: store, Build: briefingBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func startManagedBriefing(t *testing.T, ctx context.Context, runtime *agentrun.Runtime, program agentrun.Program[interaction.Input, interaction.Output]) *agentrun.Handle[interaction.Output] {
	t.Helper()
	input, err := briefingInput("Use the external expert.", briefingTopic)
	if err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Start(ctx, program, agentrun.StartRequest[interaction.Input]{ID: agentrun.StartID{Scope: "test", Key: rand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func awaitUnknown(t *testing.T, ctx context.Context, root *agentrun.Handle[interaction.Output]) {
	t.Helper()
	for entry, err := range root.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(agentrun.EffectSettled); ok && settled.Outcome.Status() == agentrun.SettlementStatusUnknown {
			return
		}
	}
	t.Fatal("interaction exited without preserving its uncertain call")
}

func loadLog(t *testing.T, ctx context.Context, store agentrun.Store, root agentrun.ProcessID) agentrun.Log {
	t.Helper()
	log, found, err := store.Load(ctx, root)
	if err != nil || !found {
		t.Fatalf("load authoritative log: found=%t error=%v", found, err)
	}
	return log
}

func unknownRequest(t *testing.T, log agentrun.Log) agentrun.EffectRequest {
	t.Helper()
	inspection, err := log.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	var requests []agentrun.EffectRequest
	for _, process := range inspection.Processes {
		for _, effect := range process.PendingEffects {
			if effect.Outcome.Status() == agentrun.SettlementStatusUnknown {
				requests = append(requests, effect.Request)
			}
		}
	}
	if len(requests) != 1 {
		t.Fatalf("unknown requests=%d, want 1", len(requests))
	}
	return requests[0]
}

func assertUnresolved(t *testing.T, log agentrun.Log) {
	t.Helper()
	inspection, err := log.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	root, found := inspection.Process(inspection.Root)
	if !found || root.Status.Terminal() {
		t.Fatal("unresolved tree published a terminal status")
	}
	for _, entry := range log.Entries {
		if _, exited := entry.Record.(agentrun.Exited); exited {
			t.Fatal("unresolved tree published Exited")
		}
	}
}
