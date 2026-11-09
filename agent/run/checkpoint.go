package run

import (
	"slices"
	"strings"
)

// Checkpoint materializes every acknowledged record before it. Recovery starts
// from the newest one without reading its prefix, so it is the authority for
// that prefix rather than a cache to be checked against it; it can only be
// written by the fenced writer that folded the prefix. Only this record carries
// opaque ExecutionState.
type Checkpoint struct {
	Processes        []ProcessCheckpoint `json:"processes"`
	StartedProcesses uint64              `json:"started_processes"`
}

func (Checkpoint) recordKind() RecordKind { return RecordKindCheckpoint }
func (c Checkpoint) valid() bool {
	if len(c.Processes) == 0 || c.StartedProcesses < uint64(len(c.Processes)) {
		return false
	}
	root := c.root()
	for index, process := range c.Processes {
		if !process.valid() || process.Started.Process.Root() != root || index > 0 && c.Processes[index-1].Started.Process.compare(process.Started.Process) >= 0 {
			return false
		}
	}
	return true
}

func (c Checkpoint) root() ProcessID {
	if len(c.Processes) == 0 {
		return ProcessID{}
	}
	return c.Processes[0].Started.Process.Root()
}

// tree restores the materialization recorded at entry. Child counters are
// derived from the captured membership instead of being stored a second time.
func (c Checkpoint) tree(root ProcessID, entry Entry) (*treeState, error) {
	if c.root() != root {
		return nil, ErrInvalidLog
	}
	state := newTreeState(root)
	state.head, state.epoch, state.startedProcesses = entry.LSN, entry.Epoch, c.StartedProcesses
	for _, captured := range c.Processes {
		id := captured.Started.Process
		if state.processes[id] != nil {
			return nil, ErrInvalidLog
		}
		state.processes[id] = captured.process()
	}
	if err := state.restoreFromCheckpoint(entry.LSN); err != nil {
		return nil, err
	}
	return state, nil
}

func (c Checkpoint) clone() Checkpoint {
	c.Processes = slices.Clone(c.Processes)
	for index := range c.Processes {
		c.Processes[index] = c.Processes[index].clone()
	}
	return c
}

type StartedChild struct {
	Key     ChildKey `json:"key"`
	Started LSN      `json:"started"`
}

type ProcessCheckpoint struct {
	Started   Started             `json:"started"`
	State     ExecutionState      `json:"state,omitzero"`
	Inbox     Inbox               `json:"inbox"`
	Receipts  []SignalReceipt     `json:"receipts"`
	Children  []StartedChild      `json:"children"`
	Waits     []WaitCheckpoint    `json:"waits"`
	Pending   []PendingCheckpoint `json:"pending"`
	Usage     Usage               `json:"usage"`
	Allocated resourceAmounts     `json:"allocated"`
	Paused    bool                `json:"paused"`
	Yielded   bool                `json:"yielded"`
	Terminal  *Termination        `json:"terminal,omitzero"`
	Exited    *Exited             `json:"exited,omitzero"`
}

// A live process is exactly one that still owns ExecutionState; an exit must
// repeat the terminal intent it completed.
func (p ProcessCheckpoint) valid() bool {
	if !p.Started.valid() || p.State.Valid() != (p.Terminal == nil) || !p.childrenValid() || !p.waitsAndReceiptsValid() || !p.pendingValid() || !p.inboxValid() || !p.resourcesValid() {
		return false
	}
	if p.Terminal != nil && (!p.Terminal.Valid() || p.Paused || p.Yielded) {
		return false
	}
	return p.Exited == nil || p.Terminal != nil && p.Exited.valid() && p.Exited.Process == p.Started.Process && p.Terminal.equal(p.Exited.Termination)
}

func (p ProcessCheckpoint) waitsAndReceiptsValid() bool {
	waits := make(map[WaitKey]WaitCheckpoint, len(p.Waits))
	for index, wait := range p.Waits {
		if !wait.Wait.Valid() || index > 0 && p.Waits[index-1].Wait.Key.String() >= wait.Wait.Key.String() || p.Terminal != nil && wait.Pending {
			return false
		}
		waits[wait.Wait.Key] = wait
	}
	answered := make(map[WaitKey]struct{})
	for index, receipt := range p.Receipts {
		if !receipt.ID.Valid() || !receipt.Digest.Valid() || index > 0 && p.Receipts[index-1].ID.String() >= receipt.ID.String() {
			return false
		}
		if receipt.Wait.Valid() {
			wait, exists := waits[receipt.Wait]
			if !exists || wait.Pending {
				return false
			}
			if _, duplicate := answered[receipt.Wait]; duplicate {
				return false
			}
			answered[receipt.Wait] = struct{}{}
		}
	}
	return true
}

func (p ProcessCheckpoint) pendingValid() bool {
	for index, pending := range p.Pending {
		id := pending.Intent.ID
		if !pending.valid() || id.Process != p.Started.Process || id.Step > p.Usage.CommittedSteps || uint64(id.Index) >= p.Usage.PreparedEffects || !p.Started.Capabilities.Allows(pending.Intent.Policy.RequiredCapabilities) || index > 0 && p.Pending[index-1].Intent.ID.compare(id) >= 0 {
			return false
		}
	}
	return uint64(len(p.Pending)) <= p.Usage.PreparedEffects
}

func (p ProcessCheckpoint) inboxValid() bool {
	signals := make(map[SignalID]struct{})
	effects := make(map[EffectID]struct{})
	children := make(map[ProcessID]struct{})
	for _, message := range p.Inbox {
		switch value := message.(type) {
		case Signal:
			index, found := slices.BinarySearchFunc(p.Receipts, value.ID, func(receipt SignalReceipt, id SignalID) int { return strings.Compare(receipt.ID.String(), id.String()) })
			if !value.Valid() || !found || p.Receipts[index] != value.receipt() {
				return false
			}
			if value.Wait.Valid() {
				index, found := slices.BinarySearchFunc(p.Waits, value.Wait, func(wait WaitCheckpoint, key WaitKey) int {
					return strings.Compare(wait.Wait.Key.String(), key.String())
				})
				if !found || p.Waits[index].Wait.admits(value.Payload) != nil {
					return false
				}
			}
			if _, duplicate := signals[value.ID]; duplicate {
				return false
			}
			signals[value.ID] = struct{}{}
		case Completion:
			if !value.Valid() || value.Effect.Process != p.Started.Process || value.Effect.Step > p.Usage.CommittedSteps || uint64(value.Effect.Index) >= p.Usage.PreparedEffects {
				return false
			}
			if _, duplicate := effects[value.Effect]; duplicate {
				return false
			}
			if _, pending := slices.BinarySearchFunc(p.Pending, value.Effect, func(pending PendingCheckpoint, id EffectID) int { return pending.Intent.ID.compare(id) }); pending {
				return false
			}
			effects[value.Effect] = struct{}{}
		case Exit:
			parent, child := value.Child.Parent()
			if !value.Valid() || !child || parent != p.Started.Process {
				return false
			}
			if _, duplicate := children[value.Child]; duplicate {
				return false
			}
			children[value.Child] = struct{}{}
		default:
			return false
		}
	}
	return resourceQuantitiesFit(p.Usage.AcceptedMessages, uint64(len(p.Receipts)), uint64(len(effects)), uint64(len(children))) && resourceQuantitiesFit(p.Usage.PreparedEffects, uint64(len(p.Pending)), uint64(len(effects)))
}

func (p ProcessCheckpoint) resourcesValid() bool {
	budget, usage, allocated := p.Started.Budget, p.Usage, p.Allocated
	if !budget.Steps.limited && allocated.Steps != 0 || !budget.Effects.limited && allocated.Effects != 0 || !budget.Messages.limited && allocated.Messages != 0 {
		return false
	}
	children := uint64(len(p.Children))
	return budget.Steps.Allows(usage.CommittedSteps, allocated.Steps) && budget.Effects.Allows(usage.PreparedEffects, allocated.Effects) && budget.Messages.Allows(usage.AcceptedMessages, allocated.Messages) && (usage.CommittedSteps != 0 || usage.PreparedEffects == 0) && usage.PreparedEffects >= children && usage.AcceptedMessages >= children
}

// childrenValid requires the canonical key order the materialization writes,
// which also rules out a key naming two children.
func (p ProcessCheckpoint) childrenValid() bool {
	for index, child := range p.Children {
		if !child.Key.Valid() || child.Started == 0 || index > 0 && p.Children[index-1].Key.String() >= child.Key.String() {
			return false
		}
	}
	return true
}

func (p ProcessCheckpoint) process() *processState {
	process := newProcessState(p.Started)
	process.inbox, process.usage, process.allocated = slices.Clone(p.Inbox), p.Usage, p.Allocated
	process.paused, process.yielded = p.Paused, p.Yielded
	for _, receipt := range p.Receipts {
		process.receipts[receipt.ID] = receipt
	}
	for _, child := range p.Children {
		process.children[child.Key] = child.Started
	}
	for _, wait := range p.Waits {
		process.waits[wait.Wait.Key] = wait
	}
	for _, pending := range p.Pending {
		process.pending[pending.Intent.ID] = pending.clone()
	}
	if p.Terminal != nil {
		process.terminal = *p.Terminal
	}
	if p.Exited != nil {
		process.exited = new(*p.Exited)
		close(process.done)
	}
	return process
}

func (p ProcessCheckpoint) clone() ProcessCheckpoint {
	p.Inbox = slices.Clone(p.Inbox)
	p.Receipts = slices.Clone(p.Receipts)
	p.Children = slices.Clone(p.Children)
	p.Waits = slices.Clone(p.Waits)
	p.Pending = slices.Clone(p.Pending)
	for index := range p.Pending {
		p.Pending[index] = p.Pending[index].clone()
	}
	if p.Terminal != nil {
		p.Terminal = new(*p.Terminal)
	}
	if p.Exited != nil {
		p.Exited = new(*p.Exited)
	}
	return p
}

type SignalReceipt struct {
	ID     SignalID `json:"id"`
	Wait   WaitKey  `json:"wait,omitzero"`
	Digest Digest   `json:"digest"`
}

// Retaining the first contract after withdrawal keeps a WaitKey immutable even
// when recovery starts after its original declaration.
type WaitCheckpoint struct {
	Wait    Wait `json:"wait"`
	Pending bool `json:"pending"`
}

type PendingCheckpoint struct {
	Intent  EffectIntent `json:"intent"`
	Outcome *Settlement  `json:"outcome,omitzero"`
	Failure *Failure     `json:"failure,omitzero"`
}

func (p PendingCheckpoint) valid() bool {
	if !p.Intent.valid() {
		return false
	}
	if p.Outcome == nil {
		return p.Failure == nil
	}
	return p.Intent.Effect.Target() == EffectTargetDispatcher && p.Outcome.Status() == SettlementStatusUnknown && (EffectSettled{ID: p.Intent.ID, Outcome: *p.Outcome, Failure: p.Failure, At: p.Intent.At}).valid()
}

func (p PendingCheckpoint) inspect(epoch Epoch, program ProgramRef) PendingEffect {
	effect := PendingEffect{Request: EffectRequest{id: p.Intent.ID, epoch: epoch, program: program, effect: p.Intent.Effect}, Policy: p.Intent.Policy}
	if p.Failure != nil {
		effect.Failure = new(*p.Failure)
	}
	if p.Outcome != nil {
		effect.Outcome = *p.Outcome
	}
	return effect
}

func (p PendingCheckpoint) clone() PendingCheckpoint {
	if p.Outcome != nil {
		p.Outcome = new(*p.Outcome)
	}
	if p.Failure != nil {
		p.Failure = new(*p.Failure)
	}
	return p
}
