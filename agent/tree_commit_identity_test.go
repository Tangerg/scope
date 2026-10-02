package agent

import (
	"os"
	"testing"
)

func TestTreeCommitCanonicalIdentityAndContent(t *testing.T) {
	boundary, checkpoint, activation := treeCommitIdentityFixture(t)
	for _, test := range []struct {
		name       string
		identity   string
		content    func() (Digest, error)
		wantKey    string
		wantDigest string
	}{
		{
			"Effect", boundary.Identity(), boundary.ContentDigest,
			"commit:dd3bd92daf50dd536c10f0820182387862285f9baf85999ef5c6ebe4d730fb52",
			"sha256:2b52e47b094954d4b2f944c6cad1c46a64979724126e1f617af8c74870f0b7f1",
		},
		{
			"checkpoint", checkpoint.Identity(), checkpoint.ContentDigest,
			"commit:b1709ed5c05ad3aad4cb95d0fbc4c7c31e7bfe42a6534b12a22c47d8918b8dda",
			"sha256:8448865961313b9a9072557dd8f0693fe7d0e40ace1510d54560d098b0933cbd",
		},
		{
			"activation", activation.Identity(), activation.ContentDigest,
			"commit:e31e5b013e3a77b32ee9f64318160c171ad470f3eb1e8b2f7e6e6f2faabec024",
			"sha256:e7c871f61cf404ee1c5e12e2375951811da8d7f64d389468e23bc2667fe0ad8c",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, err := test.content()
			if err != nil || content.String() != test.wantDigest || test.identity != test.wantKey {
				t.Fatalf("identity=%s content=%s error=%v", test.identity, content, err)
			}
		})
	}
}

func TestTreeCommitIdentityScopes(t *testing.T) {
	boundary, checkpoint, activation := treeCommitIdentityFixture(t)
	originalDigest := controlValue(boundary.ContentDigest())
	attempt := boundary
	attempt.request.attemptID = newEffectAttemptID()
	if attempt.Identity() != boundary.Identity() || controlValue(attempt.ContentDigest()) != originalDigest {
		t.Fatal("physical attempt changed a logical commit fact")
	}
	later := boundary
	later.sequence++
	if later.Identity() != boundary.Identity() || controlValue(later.ContentDigest()) == originalDigest {
		t.Fatal("Effect sequence created a new stage or lost its content precondition")
	}
	wire := controlValue(boundary.treeSnapshot.wire())
	wire.IncarnationID = newTreeIncarnationID()
	restored := boundary
	restored.treeSnapshot = controlValue(newTreeSnapshot(wire))
	restored.request.incarnationID = wire.IncarnationID
	if restored.Identity() != boundary.Identity() || controlValue(restored.ContentDigest()) == originalDigest {
		t.Fatal("activation lost cross-incarnation Effect deduplication or writer content")
	}
	next := checkpoint
	next.sequence++
	if next.Identity() == checkpoint.Identity() || controlValue(next.ContentDigest()) != controlValue(checkpoint.ContentDigest()) {
		t.Fatal("checkpoint identity followed repeated content instead of sequence")
	}
	if activation.Identity() == checkpoint.Identity() || activation.Identity() == boundary.Identity() {
		t.Fatal("activation shares another boundary's identity domain")
	}
	unknown := controlValue(NewSettlement(boundary.Request().ID(), SettlementStatusUnknown, []byte(`{"result":"uncertain"}`)))
	settled := settledIdentityFixture(t, boundary, EffectBoundaryKindSettled, unknown)
	definite := controlValue(NewSettlement(boundary.Request().ID(), SettlementStatusSucceeded, []byte(`{"result":"accepted"}`)))
	resolved := settledIdentityFixture(t, settled, EffectBoundaryKindResolved, definite)
	if boundary.Identity() == settled.Identity() || settled.Identity() == resolved.Identity() || resolved.Identity() == boundary.Identity() {
		t.Fatal("Effect stages share a commit identity")
	}
}

func TestInvalidTreeCommitHasNoIdentityOrContent(t *testing.T) {
	for _, test := range []struct {
		identity string
		content  func() (Digest, error)
	}{
		{(EffectBoundary{}).Identity(), (EffectBoundary{}).ContentDigest},
		{(EffectBoundary{kind: EffectBoundaryKindPending}).Identity(), (EffectBoundary{kind: EffectBoundaryKindPending}).ContentDigest},
		{(TreeCheckpoint{}).Identity(), (TreeCheckpoint{}).ContentDigest},
		{(TreeCheckpoint{kind: TreeCheckpointKindStart}).Identity(), (TreeCheckpoint{kind: TreeCheckpointKindStart}).ContentDigest},
		{(TreeActivation{}).Identity(), (TreeActivation{}).ContentDigest},
	} {
		content, err := test.content()
		if test.identity != "" || content.Valid() || err == nil {
			t.Fatalf("invalid commit identity=%q content=%s error=%v", test.identity, content, err)
		}
	}
}

func treeCommitIdentityFixture(t *testing.T) (EffectBoundary, TreeCheckpoint, TreeActivation) {
	t.Helper()
	data, err := os.ReadFile("testdata/tree_commit.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := controlValue(ParseTreeSnapshot(data))
	id := controlValue(ParseEffectID("effect:32bdcd1389f059414e6554afbcf0e8669d6bc06e48a99c5ba46a1d5c0b46e3f6"))
	request, found := snapshot.EffectRequest(snapshot.RootID(), id)
	if !found {
		t.Fatal("fixture has no Effect")
	}
	previous := controlValue(ParseDigest("sha256:a8fda0511f82a72f80c26b5833804253ffc8e7e731c1c19db86732c42f5f6810"))
	previousWriter := controlValue(parseTreeIncarnationID("incarnation:22222222222222222222222222222222"))
	return controlValue(newEffectBoundary(2, EffectBoundaryKindPending, request, Settlement{}, previous, snapshot)),
		controlValue(newTreeCheckpoint(2, TreeCheckpointKindProgress, previous, snapshot)),
		controlValue(newTreeActivation(previousWriter, previous, snapshot.IncarnationID(), snapshot))
}

func settledIdentityFixture(t *testing.T, boundary EffectBoundary, kind EffectBoundaryKind, settlement Settlement) EffectBoundary {
	t.Helper()
	wire := controlValue(boundary.treeSnapshot.wire())
	process := controlValue(wire.ProcessSnapshots[0].wire())
	process.Prepared.Effects[0].Phase = effectPhaseSettled
	process.Prepared.Effects[0].Settlement = &settlement
	wire.ProcessSnapshots[0] = controlValue(newProcessSnapshot(process))
	snapshot := controlValue(newTreeSnapshot(wire))
	return controlValue(newEffectBoundary(boundary.sequence+1, kind, boundary.request, settlement, boundary.treeSnapshot.Digest(), snapshot))
}
