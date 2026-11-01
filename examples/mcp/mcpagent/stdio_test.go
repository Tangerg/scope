//go:build unix

package main

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
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
			deployment := peerDeployment(t, ctx, peer, permit, briefingQuery)
			engine := managedEngine(t, agentrun.NewMemoryStore(), nil)
			brief, runErr := runBriefing(ctx, engine, deployment, prompt, briefingTopic)
			if runErr != nil {
				t.Fatal(runErr)
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
		engine := managedEngine(t, store, nil)
		root := startManagedBriefing(t, ctx, engine, peerDeployment(t, ctx, peer, true, expertWaitQuery))
		peer.await(t, ctx, expertStarted)
		if err := root.Cancel(ctx, "stop the external search"); err != nil {
			t.Fatal(err)
		}
		result, err := root.Await(ctx)
		if err != nil || result.Termination().Status() != agentrun.StatusCanceled {
			t.Fatalf("cancellation status=%s error=%v", result.Termination().Status(), err)
		}
		if err := root.Join(ctx); err != nil {
			t.Fatal(err)
		}
		peer.await(t, ctx, expertCanceled)
		if status := peer.status(t, ctx); status.Calls != 1 || status.Canceled != 1 {
			t.Fatalf("peer cancellation=%+v", status)
		}
		unknownRequest(t, loadHead(t, ctx, store, root.Relation().ProcessID()))
	})
	t.Run("reconnection cannot replay or settle an uncertain call", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		peer := openExpert(t, ctx, binary)
		store := agentrun.NewMemoryStore()
		observations := &runtest.ObservationRecorder{}
		engine := managedEngine(t, store, observations)
		deployment := peerDeployment(t, ctx, peer, true, expertDisconnectQuery)
		root := startManagedBriefing(t, ctx, engine, deployment)
		peer.await(t, ctx, expertStarted)
		if _, err := observations.AwaitEvent(ctx, func(event agentrun.Event) bool {
			fact, ok := event.EffectFinished()
			return ok && fact.SettlementStatus() == agentrun.SettlementStatusUnknown
		}); err != nil {
			t.Fatal(err)
		}
		if result, err := peer.command.Wait(); err != nil || result.ExitCode != expertExitCode {
			t.Fatalf("expert exit=%+v error=%v", result, err)
		}
		if err := root.Pause(ctx, "handoff before reconnecting"); err != nil {
			t.Fatal(err)
		}
		if _, err := observations.AwaitEvent(ctx, func(event agentrun.Event) bool {
			return event.Name() == agentrun.EventProcessPaused && event.Relation().IsRoot()
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.CaptureTree(ctx, root.Relation().ProcessID()); err != nil {
			t.Fatal(err)
		}
		head := loadHead(t, ctx, store, root.Relation().ProcessID())
		original := unknownRequest(t, head)
		reconnected := openExpert(t, ctx, binary)
		replacement := managedEngine(t, store, nil)
		changedGrant := peerDeployment(t, ctx, reconnected, false, expertDisconnectQuery)
		if changedGrant.ProgramRef() == deployment.ProgramRef() {
			t.Fatal("authorization change did not change the exact binding")
		}
		if _, err := replacement.Recover(ctx, changedGrant, head); !errors.Is(err, agentrun.ErrInvalidTreeSnapshot) {
			t.Fatalf("restoration with changed authorization=%v", err)
		}
		if loaded := loadHead(t, ctx, store, head.RootID()); loaded.Digest() != head.Digest() {
			t.Fatal("rejected restoration advanced the authoritative head")
		}
		exact := peerDeployment(t, ctx, reconnected, true, expertDisconnectQuery)
		if exact.ProgramRef() != deployment.ProgramRef() {
			t.Fatal("session identity leaked into the deployment binding")
		}
		restored, err := replacement.Recover(ctx, exact, head)
		if err != nil {
			t.Fatal(err)
		}
		cleanupRoot(t, restored)
		if resumeErr := restored.Resume(ctx); resumeErr != nil {
			t.Fatal(resumeErr)
		}
		cut, err := replacement.CaptureTree(ctx, restored.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		if current := unknownRequest(t, cut); current.ID() != original.ID() || current.Relation().ProcessID() != original.Relation().ProcessID() {
			t.Fatal("reconnection changed the uncertain Effect identity")
		}
		if status := reconnected.status(t, ctx); status.Calls != 0 {
			t.Fatalf("recovery replayed an uncertain call: %+v", status)
		}
		if cancelErr := restored.Cancel(ctx, "leave the remote outcome unresolved"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		result, err := restored.Await(ctx)
		if err != nil || result.Termination().Status() != agentrun.StatusCanceled {
			t.Fatalf("restored cancellation=%s error=%v", result.Termination().Status(), err)
		}
		if joinErr := restored.Join(ctx); joinErr != nil {
			t.Fatal(joinErr)
		}
		terminal := loadHead(t, ctx, store, head.RootID())
		if unknownRequest(t, terminal).ID() != original.ID() {
			t.Fatal("cancellation erased uncertainty")
		}
		fresh := peerDeployment(t, ctx, reconnected, true, briefingQuery)
		brief, err := runBriefing(ctx, replacement, fresh, "Use the external expert.", briefingTopic)
		if err != nil || !slices.Equal(brief.Sources, []string{expertSourceURL}) {
			t.Fatalf("fresh task after reconnect=%+v error=%v", brief, err)
		}
		reconnected.await(t, ctx, expertStarted)
		if status := reconnected.status(t, ctx); status.Calls != 1 {
			t.Fatalf("fresh task calls=%+v", status)
		}
		if loadHead(t, ctx, store, head.RootID()).Digest() != terminal.Digest() {
			t.Fatal("a fresh task advanced the old execution")
		}
	})
}

func peerDeployment(t *testing.T, ctx context.Context, peer *stdioPeer, permit bool, query string) agentrun.Program[agentrun.Payload, agentrun.Payload] {
	t.Helper()
	deployment, err := newBriefingDeployment(ctx, briefingConfig{Session: peer.session, ExpertDigest: peer.artifact.digest, AllowSearch: permit, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func managedEngine(t *testing.T, store *agentrun.MemoryStore, recorder *runtest.ObservationRecorder) *agentrun.Runtime {
	t.Helper()
	config := agentrun.Config{Store: store}
	if recorder != nil {
		config.EventListeners = []agentrun.EventListener{recorder}
	}
	engine, err := agentrun.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := engine.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return engine
}

func startManagedBriefing(t *testing.T, ctx context.Context, engine *agentrun.Runtime, deployment agentrun.Program[agentrun.Payload, agentrun.Payload]) *agentrun.Handle[agentrun.Payload] {
	t.Helper()
	input, err := briefingInput("Use the external expert.", briefingTopic)
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(ctx, deployment, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	cleanupRoot(t, root)
	return root
}

func cleanupRoot(t *testing.T, root *agentrun.Handle[agentrun.Payload]) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := root.Kill(ctx, "test cleanup"); err != nil && !errors.Is(err, agentrun.ErrProcessFinished) && !errors.Is(err, agentrun.ErrTreeIncarnationConflict) {
			t.Error(err)
		}
		if err := root.Join(ctx); err != nil && !errors.Is(err, agentrun.ErrTreeIncarnationConflict) {
			t.Error(err)
		}
	})
}

func loadHead(t *testing.T, ctx context.Context, store *agentrun.MemoryStore, root agentrun.ProcessID) agentrun.TreeSnapshot {
	t.Helper()
	head, found, err := store.LoadTree(ctx, root)
	if err != nil || !found {
		t.Fatalf("load authoritative head: found=%t error=%v", found, err)
	}
	return head
}

func unknownRequest(t *testing.T, head agentrun.TreeSnapshot) agentrun.EffectRequest {
	t.Helper()
	var requests []agentrun.EffectRequest
	for _, snapshot := range head.ProcessSnapshots() {
		for _, id := range snapshot.UnknownEffectIDs() {
			request, found := head.EffectRequest(snapshot.Relation().ProcessID(), id)
			if !found {
				t.Fatal("missing captured Effect request")
			}
			requests = append(requests, request)
		}
	}
	if len(requests) != 1 {
		t.Fatalf("unknown requests=%d, want 1", len(requests))
	}
	return requests[0]
}
