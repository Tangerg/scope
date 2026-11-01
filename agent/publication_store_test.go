package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

type storedResult struct {
	Reference agent.ToolCallRef
	Entry     agent.ResultEntry
}

type publicationDatabase struct {
	Start        *run.StartReceipt `json:",omitzero"`
	Tree         run.TreeSnapshot
	Sequence     uint64
	Publications map[string]json.RawMessage
	Transactions map[string]run.Digest
	Results      []storedResult
}

type publicationCommit struct {
	start          run.StartReceipt
	snapshot       run.TreeSnapshot
	previous       run.Digest
	expectedWriter run.TreeIncarnationID
	identity       string
	content        run.Digest
	sequence       uint64
}

// publicationStore models a host that chooses to keep a separate result history.
// The framework's committer contract requires only the authoritative tree cut.
type publicationStore struct {
	mu       sync.Mutex
	path     string
	closed   bool
	database publicationDatabase
	before   func(run.TreeSnapshot, []agent.RoundResults) error
	after    func(run.TreeSnapshot, []agent.RoundResults) error
}

func newPublicationStore(t *testing.T) *publicationStore {
	t.Helper()
	return openPublicationStore(t, filepath.Join(t.TempDir(), "publication.json"))
}

func openPublicationStore(t *testing.T, path string) *publicationStore {
	t.Helper()
	store := &publicationStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		store.database = publicationDatabase{Publications: make(map[string]json.RawMessage), Transactions: make(map[string]run.Digest)}
	} else if err != nil {
		t.Fatal(err)
	} else if err := jsonv2.Unmarshal(data, &store.database); err != nil {
		t.Fatal(err)
	}
	return store
}

func (p *publicationStore) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.database = publicationDatabase{}
}

func (p *publicationStore) tree() run.TreeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.database.Tree
}

func (p *publicationStore) LoadTree(ctx context.Context, rootID run.ProcessID) (run.TreeSnapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.TreeSnapshot{}, false, err
	}
	if p.closed {
		return run.TreeSnapshot{}, false, os.ErrClosed
	}
	if !rootID.Valid() {
		return run.TreeSnapshot{}, false, run.ErrInvalidTreeSnapshot
	}
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return run.TreeSnapshot{}, false, nil
	}
	if err != nil {
		return run.TreeSnapshot{}, false, err
	}
	var database publicationDatabase
	if err := jsonv2.Unmarshal(data, &database, jsonv2.RejectUnknownMembers(true)); err != nil {
		return run.TreeSnapshot{}, false, err
	}
	if database.Tree.RootID() != rootID {
		return run.TreeSnapshot{}, false, nil
	}
	return database.Tree, true, nil
}

func (p *publicationStore) entries() []agent.ResultEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var entries []agent.ResultEntry
	for _, result := range p.database.Results {
		entries = append(entries, result.Entry)
	}
	return entries
}

func (p *publicationStore) ActivateTree(_ context.Context, activation run.TreeActivation) error {
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(publicationCommit{
		snapshot: activation.TreeSnapshot(), previous: activation.PreviousTreeDigest(),
		expectedWriter: activation.PreviousIncarnationID(), identity: activation.Identity(), content: content,
	})
}

func (p *publicationStore) CommitEffect(_ context.Context, boundary run.EffectBoundary) error {
	content, err := boundary.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(publicationCommit{
		snapshot: boundary.TreeSnapshot(), previous: boundary.PreviousTreeDigest(),
		expectedWriter: boundary.TreeSnapshot().IncarnationID(), identity: boundary.Identity(), content: content,
		sequence: boundary.Sequence(),
	})
}

func (p *publicationStore) CommitCheckpoint(_ context.Context, checkpoint run.TreeCheckpoint) error {
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	start, _ := checkpoint.Start()
	return p.commit(publicationCommit{start: start,
		snapshot: checkpoint.TreeSnapshot(), previous: checkpoint.PreviousTreeDigest(),
		expectedWriter: checkpoint.TreeSnapshot().IncarnationID(), identity: checkpoint.Identity(), content: content,
		sequence: checkpoint.Sequence(),
	})
}

func (p *publicationStore) commit(proposal publicationCommit) error {
	snapshot := proposal.snapshot
	publications, err := agent.SettledResults(snapshot)
	if err != nil {
		return err
	}
	if p.before != nil {
		if beforeErr := p.before(snapshot, publications); beforeErr != nil {
			return beforeErr
		}
	}
	p.mu.Lock()
	err = p.advance(proposal, publications)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if p.after != nil {
		return p.after(snapshot, publications)
	}
	return nil
}

func (p *publicationStore) advance(proposal publicationCommit, publications []agent.RoundResults) error {
	if p.closed {
		return os.ErrClosed
	}
	snapshot := proposal.snapshot
	head := p.database.Tree
	if proposal.sequence > 0 && proposal.previous.Valid() && head.IncarnationID() != proposal.expectedWriter {
		return run.ErrTreeIncarnationConflict
	}
	if stored, exists := p.database.Transactions[proposal.identity]; exists {
		if stored != proposal.content || p.database.Sequence != proposal.sequence || head.Digest() != snapshot.Digest() {
			return run.ErrCommitConflict
		}
		return nil
	}
	if !proposal.previous.Valid() {
		if head.Valid() {
			return run.ErrTreeIncarnationConflict
		}
	} else {
		if !head.Valid() || head.IncarnationID() != proposal.expectedWriter || head.Digest() != proposal.previous {
			return run.ErrTreeIncarnationConflict
		}
		if proposal.sequence > 0 && (p.database.Sequence == math.MaxUint64 || proposal.sequence != p.database.Sequence+1) {
			return run.ErrTreeIncarnationConflict
		}
	}
	// The proposal is committed as one file replacement. No process memory is
	// needed after reopening to recover either the tree or its product projection.
	candidate := publicationDatabase{Start: p.database.Start, Tree: snapshot, Sequence: proposal.sequence, Publications: make(map[string]json.RawMessage), Transactions: make(map[string]run.Digest), Results: append([]storedResult(nil), p.database.Results...)}
	if proposal.start.Valid() {
		v := proposal.start
		candidate.Start = &v
	}
	for id, data := range p.database.Publications {
		candidate.Publications[id] = data
	}
	for id, digest := range p.database.Transactions {
		candidate.Transactions[id] = digest
	}
	for _, publication := range publications {
		if err := candidate.recordPublication(publication.Digest(), publication.JSON()); err != nil {
			return err
		}
		for _, entry := range publication.Entries() {
			reference, present := publication.Reference(entry.ToolCallIndex)
			if !present {
				return agent.ErrInvalidToolCallRef
			}
			found := false
			for _, existing := range candidate.Results {
				if existing.Reference == reference {
					oldData, err := jsonv2.Marshal(existing.Entry)
					if err != nil {
						return err
					}
					newData, err := jsonv2.Marshal(entry)
					if err != nil {
						return err
					}
					if !bytes.Equal(oldData, newData) {
						return run.ErrCommitConflict
					}
					found = true
					break
				}
			}
			if !found {
				candidate.Results = append(candidate.Results, storedResult{Reference: reference, Entry: entry})
			}
		}
	}
	candidate.Transactions[proposal.identity] = proposal.content
	data, err := jsonv2.Marshal(candidate)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(p.path), "transaction-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, p.path); err != nil {
		return err
	}
	p.database = candidate
	return nil
}

func (p *publicationDatabase) recordPublication(id run.Digest, payload json.RawMessage) error {
	if run.ComputeDigest(payload) != id {
		return run.ErrCommitConflict
	}
	if previous, found := p.Publications[id.String()]; found && !bytes.Equal(previous, payload) {
		return run.ErrCommitConflict
	}
	p.Publications[id.String()] = bytes.Clone(payload)
	return nil
}

func publicationEngine(t *testing.T, deployment interactionDeployment, store run.Store) *run.Runtime {
	t.Helper()
	engine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return engine
}

func publicationText(entry agent.ResultEntry) string {
	text, _ := entry.Result.Output.Text()
	return text
}

func TestPublicationStoreTreeCommitterConformance(t *testing.T) {
	runtest.RunTreeCommitterConformance(t, func() runtest.TreeCommitterConformanceDriver {
		return newPublicationStore(t)
	})
}

func (p *publicationStore) LookupStart(_ context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.database.Start != nil && p.database.Start.ID == id {
		return *p.database.Start, true, nil
	}
	return run.StartReceipt{}, false, nil
}
