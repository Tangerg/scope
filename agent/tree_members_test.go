package agent

import (
	"slices"
	"testing"
)

func TestTreeMembersKeepTheParentIndexExact(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	members := runtime.members
	children := slices.Clone(members.childrenOf(runtime.rootID))
	if len(children) != 2 || members.len() != 3 {
		t.Fatalf("children=%v members=%d", children, members.len())
	}
	members.remove(children[0])
	if got := members.childrenOf(runtime.rootID); !slices.Equal(got, children[1:]) || members.get(children[0]) != nil {
		t.Fatalf("removal left children %v", got)
	}
	members.remove(children[1])
	if _, grouped := members.children[runtime.rootID]; grouped || members.len() != 1 {
		t.Fatal("empty parent group outlived its last child")
	}
}

func TestTreeMembersSubstituteCandidatesAndAdmitNewOnes(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	root := runtime.members.get(runtime.rootID)
	replacement := root.candidate()
	child := runtime.members.get(runtime.members.childrenOf(runtime.rootID)[0])
	runtime.removeProcess(child.handle.processID())
	var yielded []*processState
	for member := range runtime.members.substituted([]*processState{replacement, child}) {
		yielded = append(yielded, member)
	}
	if len(yielded) != 2 || !slices.Contains(yielded, replacement) || slices.Contains(yielded, root) || !slices.Contains(yielded, child) {
		t.Fatalf("substituted cut = %v", yielded)
	}
}
