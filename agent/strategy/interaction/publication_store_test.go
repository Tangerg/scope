package interaction_test

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

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
)

type storedResult struct {
	Reference interaction.ToolCallRef
	Entry     interaction.ResultEntry
}

type publicationDatabase struct {
	Tree         agent.TreeSnapshot
	Sequence     uint64
	Publications map[string]json.RawMessage
	Transactions map[string]agent.Digest
	Results      []storedResult
}

type publicationCommit struct {
	snapshot       agent.TreeSnapshot
	previous       agent.Digest
	expectedWriter agent.TreeIncarnationID
	identity       string
	content        agent.Digest
	sequence       uint64
}

// publicationStore models a host that chooses to keep a separate result history.
// The framework's committer contract requires only the authoritative tree cut.
type publicationStore struct {
	mu       sync.Mutex
	path     string
	closed   bool
	database publicationDatabase
	before   func(agent.TreeSnapshot, []interaction.RoundResults) error
	after    func(agent.TreeSnapshot, []interaction.RoundResults) error
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
		store.database = publicationDatabase{Publications: make(map[string]json.RawMessage), Transactions: make(map[string]agent.Digest)}
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

func (p *publicationStore) tree() agent.TreeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.database.Tree
}

func (p *publicationStore) LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.TreeSnapshot{}, false, err
	}
	if p.closed {
		return agent.TreeSnapshot{}, false, os.ErrClosed
	}
	if !rootID.Valid() {
		return agent.TreeSnapshot{}, false, agent.ErrInvalidTreeSnapshot
	}
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return agent.TreeSnapshot{}, false, nil
	}
	if err != nil {
		return agent.TreeSnapshot{}, false, err
	}
	var database publicationDatabase
	if err := jsonv2.Unmarshal(data, &database, jsonv2.RejectUnknownMembers(true)); err != nil {
		return agent.TreeSnapshot{}, false, err
	}
	if database.Tree.RootID() != rootID {
		return agent.TreeSnapshot{}, false, nil
	}
	return database.Tree, true, nil
}

func (p *publicationStore) entries() []interaction.ResultEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var entries []interaction.ResultEntry
	for _, result := range p.database.Results {
		entries = append(entries, result.Entry)
	}
	return entries
}

func (p *publicationStore) ActivateTree(_ context.Context, activation agent.TreeActivation) error {
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(publicationCommit{
		snapshot: activation.TreeSnapshot(), previous: activation.PreviousTreeDigest(),
		expectedWriter: activation.PreviousIncarnationID(), identity: activation.Identity(), content: content,
	})
}

func (p *publicationStore) CommitEffect(_ context.Context, boundary agent.EffectBoundary) error {
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

func (p *publicationStore) CommitCheckpoint(_ context.Context, checkpoint agent.TreeCheckpoint) error {
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(publicationCommit{
		snapshot: checkpoint.TreeSnapshot(), previous: checkpoint.PreviousTreeDigest(),
		expectedWriter: checkpoint.TreeSnapshot().IncarnationID(), identity: checkpoint.Identity(), content: content,
		sequence: checkpoint.Sequence(),
	})
}

func (p *publicationStore) commit(proposal publicationCommit) error {
	snapshot := proposal.snapshot
	publications, err := interaction.SettledResults(snapshot)
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

func (p *publicationStore) advance(proposal publicationCommit, publications []interaction.RoundResults) error {
	if p.closed {
		return os.ErrClosed
	}
	snapshot := proposal.snapshot
	head := p.database.Tree
	if proposal.sequence > 0 && proposal.previous.Valid() && head.IncarnationID() != proposal.expectedWriter {
		return agent.ErrTreeIncarnationConflict
	}
	if stored, exists := p.database.Transactions[proposal.identity]; exists {
		if stored != proposal.content || p.database.Sequence != proposal.sequence || head.Digest() != snapshot.Digest() {
			return agent.ErrCommitConflict
		}
		return nil
	}
	if !proposal.previous.Valid() {
		if head.Valid() {
			return agent.ErrTreeIncarnationConflict
		}
	} else {
		if !head.Valid() || head.IncarnationID() != proposal.expectedWriter || head.Digest() != proposal.previous {
			return agent.ErrTreeIncarnationConflict
		}
		if proposal.sequence > 0 && (p.database.Sequence == math.MaxUint64 || proposal.sequence != p.database.Sequence+1) {
			return agent.ErrTreeIncarnationConflict
		}
	}
	// The proposal is committed as one file replacement. No process memory is
	// needed after reopening to recover either the tree or its product projection.
	candidate := publicationDatabase{Tree: snapshot, Sequence: proposal.sequence, Publications: make(map[string]json.RawMessage), Transactions: make(map[string]agent.Digest), Results: append([]storedResult(nil), p.database.Results...)}
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
				return interaction.ErrInvalidToolCallRef
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
						return agent.ErrCommitConflict
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

func (p *publicationDatabase) recordPublication(id agent.Digest, payload json.RawMessage) error {
	if agent.ComputeDigest(payload) != id {
		return agent.ErrCommitConflict
	}
	if previous, found := p.Publications[id.String()]; found && !bytes.Equal(previous, payload) {
		return agent.ErrCommitConflict
	}
	p.Publications[id.String()] = bytes.Clone(payload)
	return nil
}

func publicationEngine(t *testing.T, deployment interactionDeployment, store agent.TreeCommitter) *agent.Engine {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver, TreeCommitter: store})
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

func publicationText(entry interaction.ResultEntry) string {
	text, _ := entry.Result.Output.Text()
	return text
}

func TestPublicationStoreTreeCommitterConformance(t *testing.T) {
	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
		return newPublicationStore(t)
	})
}
