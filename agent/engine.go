package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/samber/lo"
)

var (
	ErrInvalidEngineConfig         = errors.New("agent: invalid engine configuration")
	ErrEngineClosed                = errors.New("agent: engine is closed")
	ErrEngineQuiescenceUnavailable = errors.New("agent: engine cannot become quiescent")
	ErrEngineHasActiveProcesses    = errors.New("agent: engine has active processes")
	ErrProcessAlreadyExists        = errors.New("agent: process identity already exists")
)

// EngineConfig keeps scheduling and authority policy outside Deployments so a
// strategy cannot change its constraints through its behavior binding. Limits,
// TreeLimits, and Capabilities apply to newly started root trees. RestoreTree
// retains their captured values; the Host authorizes snapshots before recovery.
type EngineConfig struct {
	// TreeDurability makes publication wait for acknowledgment of a recoverable
	// tree. Nil selects ephemeral execution without storage acknowledgment.
	TreeDurability TreeDurability

	// ProcessInitializationOutcomeAcknowledger optionally accepts initialization outcomes
	// before publication. Its acknowledgment is separate from TreeDurability;
	// nil omits this Host acceptance step.
	ProcessInitializationOutcomeAcknowledger ProcessInitializationOutcomeAcknowledger

	// Exact local bindings prevent restoration from silently selecting different
	// behavior. Same-Deployment recursion needs no resolver.
	DeploymentResolver DeploymentResolver

	// ProcessAdmitter optionally applies Host policy before root or child
	// initialization. Nil admits every start that satisfies Engine constraints.
	ProcessAdmitter ProcessAdmitter

	// EventListeners receive synchronous Framework facts. An empty slice disables
	// delivery. Callbacks must be bounded and must not query or control their tree.
	EventListeners []EventListener

	// DeltaListeners receive queued, best-effort Strategy increments. An empty
	// slice disables delivery. Callbacks run serially and must return so Engine
	// shutdown can drain the queue and join its delivery worker.
	DeltaListeners []DeltaListener

	// A bounded queue prevents slow listeners from retaining unlimited Deltas.
	// Zero selects the library default; negative capacities are invalid.
	DeltaBufferCapacity int

	// Zero fields inherit DefaultLimits so partial overrides still produce
	// complete per-Process resource bounds.
	Limits Limits

	// TreeLimits bounds descendant count, depth, and active children. Zero fields
	// inherit DefaultTreeLimits independently of per-Process Limits.
	TreeLimits TreeLimits

	// Capabilities grants authority to new roots. Children receive only subsets
	// of their parent's captured authority, including after restoration.
	Capabilities CapabilitySet
}

// Engine keeps admission, publication, and execution under one owner because
// resource reservations and recoverable tree state must describe the same
// lifecycle. Construct it with NewEngine; copying an Engine would share its
// registries while duplicating their synchronization.
type Engine struct {
	durability                        TreeDurability
	initializationOutcomeAcknowledger ProcessInitializationOutcomeAcknowledger
	resolver                          DeploymentResolver
	admitter                          ProcessAdmitter
	observation                       *observationBus
	limits                            Limits
	treeLimits                        TreeLimits
	capabilities                      CapabilitySet

	// Tree-wide administrative operations use an independent serial lane so a
	// caller waiting for one root never holds the registry lock needed by Engine
	// lifecycle paths. The two locks are therefore never nested.
	treeOperationsMu sync.Mutex
	treeOperations   map[ProcessID]*treeOperation

	// Registry and reservation changes share this lock so publication cannot
	// expose a Process whose admission still appears unreserved.
	mu                      sync.RWMutex
	processes               map[ProcessID]*processHandleState
	trees                   map[ProcessID]*treeRuntime
	startReservations       map[ProcessID]processStartReservation
	treeRestoreReservations map[ProcessID]*treeRestoration
	children                map[childIdentity]ProcessID
	childStartReservations  map[childIdentity]ProcessID
	// One channel closes admission at allocation and joins completion on close,
	// avoiding separate shutdown flags that could disagree.
	closeDone chan struct{}
}

// ObservationFailures returns consistent panic counts and the latest bounded
// diagnostic for each listener kind. Listener failures cannot veto execution.
func (e *Engine) ObservationFailures() ObservationFailures {
	if e == nil || e.observation == nil {
		return ObservationFailures{}
	}
	return e.observation.failureSnapshot()
}

// FlushDeltas provides the ordering barrier needed before publishing a final
// value that must not overtake accepted streaming observations. Dropped Deltas
// remain lost because flushing cannot strengthen best-effort delivery. Closing
// the Engine prevents new flush barriers, even when no listeners are configured.
func (e *Engine) FlushDeltas(ctx context.Context) error {
	if e == nil {
		return ErrEngineClosed
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkDeltaListenerReentrancy(ctx, "FlushDeltas"); err != nil {
		return err
	}
	e.mu.RLock()
	closing := e.closeDone != nil
	e.mu.RUnlock()
	if closing {
		return ErrEngineClosed
	}
	return e.observation.flushDeltas(ctx)
}

// NewEngine validates the whole configuration up front because an Engine owns
// Process lifecycle: a defect discovered after Processes exist has no safe
// remedy, since stopping the Engine would abandon in-flight effects whose
// settlement is still unknown.
func NewEngine(config EngineConfig) (*Engine, error) {
	if config.DeltaBufferCapacity < 0 {
		return nil, fmt.Errorf("%w: DeltaBufferCapacity must not be negative", ErrInvalidEngineConfig)
	}
	if config.TreeDurability != nil && lo.IsNil(config.TreeDurability) {
		return nil, fmt.Errorf("%w: TreeDurability is typed nil", ErrInvalidEngineConfig)
	}
	if config.ProcessInitializationOutcomeAcknowledger != nil && lo.IsNil(config.ProcessInitializationOutcomeAcknowledger) {
		return nil, fmt.Errorf("%w: ProcessInitializationOutcomeAcknowledger is typed nil", ErrInvalidEngineConfig)
	}
	if config.DeploymentResolver != nil && lo.IsNil(config.DeploymentResolver) {
		return nil, fmt.Errorf("%w: DeploymentResolver is typed nil", ErrInvalidEngineConfig)
	}
	if config.ProcessAdmitter != nil && lo.IsNil(config.ProcessAdmitter) {
		return nil, fmt.Errorf("%w: ProcessAdmitter is typed nil", ErrInvalidEngineConfig)
	}
	for index, listener := range config.EventListeners {
		if lo.IsNil(listener) {
			return nil, fmt.Errorf("%w: EventListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	for index, listener := range config.DeltaListeners {
		if lo.IsNil(listener) {
			return nil, fmt.Errorf("%w: DeltaListeners[%d] is nil", ErrInvalidEngineConfig, index)
		}
	}
	capacity := config.DeltaBufferCapacity
	if capacity == 0 {
		capacity = defaultDeltaBuffer
	}
	limits, err := config.Limits.resolve()
	if err != nil {
		return nil, err
	}
	treeLimits, err := config.TreeLimits.resolve()
	if err != nil {
		return nil, err
	}
	if !config.Capabilities.Valid() {
		return nil, fmt.Errorf("%w: capabilities are invalid", ErrInvalidEngineConfig)
	}
	return &Engine{
		durability:                        config.TreeDurability,
		initializationOutcomeAcknowledger: config.ProcessInitializationOutcomeAcknowledger,
		resolver:                          config.DeploymentResolver,
		admitter:                          config.ProcessAdmitter,
		observation:                       newObservationBus(config.EventListeners, config.DeltaListeners, capacity),
		limits:                            limits,
		treeLimits:                        treeLimits,
		capabilities:                      config.Capabilities,
		treeOperations:                    make(map[ProcessID]*treeOperation),
		processes:                         make(map[ProcessID]*processHandleState),
		trees:                             make(map[ProcessID]*treeRuntime),
		startReservations:                 make(map[ProcessID]processStartReservation),
		treeRestoreReservations:           make(map[ProcessID]*treeRestoration),
		children:                          make(map[childIdentity]ProcessID),
		childStartReservations:            make(map[childIdentity]ProcessID),
	}, nil
}

type childIdentity struct {
	parent ProcessID
	key    ChildKey
}

// Start keeps ctx attached to the resulting tree so Host cancellation and
// deadlines reach accepted work. Execution that outlives a request therefore
// needs a longer-lived context.
func (e *Engine) Start(ctx context.Context, deployment Deployment, input Input) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = requireContext(ctx)
	if err := deployment.validateDefinition(); err != nil {
		return nil, err
	}
	if err := deployment.Descriptor().ValidateInput(input); err != nil {
		return nil, err
	}
	id, err := newProcessID()
	if err != nil {
		return nil, err
	}
	relation := rootProcessRelation(id)
	budget := budgetFromLimits(e.limits)
	admission := newProcessAdmission(relation, deployment, budget, e.capabilities)
	if reserveProcessStartErr := e.reserveProcessStart(
		relation, deployment.DeploymentRef(), e.treeLimits, Digest{},
	); reserveProcessStartErr != nil {
		return nil, reserveProcessStartErr
	}
	published := false
	defer func() {
		if !published {
			e.discardProcessStartReservation(id)
		}
	}()
	if requestProcessAdmissionErr := requestProcessAdmission(ctx, e.admitter, admission); requestProcessAdmissionErr != nil {
		return nil, requestProcessAdmissionErr
	}
	startedAt := time.Now().Round(0).UTC()
	execution, state, failure, err := initializeExecution(deployment.Definition(), input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitializationOutcome(ctx, e.initializationOutcomeAcknowledger, failedProcessInitializationOutcome(admission, failure))
		return nil, errors.Join(fmt.Errorf("agent: initialize Process: %w", err), acknowledgeErr)
	}
	if err := acknowledgeProcessInitializationOutcome(ctx, e.initializationOutcomeAcknowledger, initializedProcessOutcome(admission, startedAt)); err != nil {
		return nil, err
	}
	handle := newProcessHandleState(
		relation, deployment.DeploymentRef(), budget, e.capabilities,
		e.treeLimits,
		startedAt, StatusRunning,
	)
	process := newProcessState(handle, deployment, execution, state, startedAt, e.limits)
	runtime := newTreeRuntime(e, relation.RootID(), ctx, process)
	if e.durability != nil {
		incarnation, incarnationErr := newTreeIncarnationID()
		if incarnationErr != nil {
			return nil, incarnationErr
		}
		runtime.incarnation = incarnation
		baseSnapshot, captureErr := runtime.captureTree()
		if captureErr != nil {
			return nil, captureErr
		}
		checkpoint, err := newTreeCheckpoint(TreeCheckpointStart, Digest{}, baseSnapshot)
		if err != nil {
			return nil, err
		}
		if err := commitTreeCheckpoint(ctx, e.durability, checkpoint); err != nil {
			return nil, err
		}
		runtime.establishDurableHead(incarnation, baseSnapshot)
	}
	e.publishReservedProcess(handle)
	published = true
	go runtime.run(ctx)
	return &Process{handle: handle}, nil
}

// Run starts one root Process and joins its entire subtree before returning the
// root's terminal Result. It keeps waiting after ctx cancellation because owned
// work and required acknowledgments must finish. A runtime failure anywhere in
// the subtree returns a RuntimeError and no Result, even when the root already
// completed; its acknowledged result remains available through Process.Await.
// Ordinary execution failure returns the root's valid Result and nil error.
func (e *Engine) Run(ctx context.Context, deployment Deployment, input Input) (Result, error) {
	process, err := e.Start(ctx, deployment, input)
	if err != nil {
		return Result{}, err
	}
	waitContext := context.WithoutCancel(requireContext(ctx))
	if err := process.Join(waitContext); err != nil {
		return Result{}, err
	}
	return process.Await(waitContext)
}

func (e *Engine) Process(id ProcessID) (*Process, bool) {
	if e == nil || !id.Valid() {
		return nil, false
	}
	e.mu.RLock()
	handle, exists := e.processes[id]
	e.mu.RUnlock()
	if !exists {
		return nil, false
	}
	return &Process{handle: handle}, true
}

// Close rejects pending starts or restorations, Processes whose result
// publication or parent/child bookkeeping is incomplete, and trees that still
// own asynchronous work or a freeze. Process.Join or Run establishes subtree
// completion; callers must finish every tree and release any freeze before
// closing the Engine.
// Once closing begins, the Engine drains accepted Delta delivery and stops
// observation workers. Canceling ctx stops only this caller's wait; it does not
// interrupt that owned shutdown. A later Close joins the same shutdown.
// Concurrent callers join the same closure; existing handles retain results
// and RuntimeErrors for later reads.
func (e *Engine) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkDeltaListenerReentrancy(ctx, "Close"); err != nil {
		return err
	}
	done, err := e.startClose()
	if err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) startClose() (<-chan struct{}, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return e.closeDone, nil
	}
	if len(e.startReservations) != 0 || len(e.treeRestoreReservations) != 0 {
		return nil, fmt.Errorf("%w: Process publication is pending", ErrEngineHasActiveProcesses)
	}
	for _, handle := range e.processes {
		select {
		case <-handle.bookkeepingDone:
		default:
			return nil, fmt.Errorf(
				"%w: Process %s has an unpublished outcome or pending parent/child bookkeeping",
				ErrEngineHasActiveProcesses, handle.processID,
			)
		}
	}
	for rootID, runtime := range e.trees {
		if runtime.inFlightWork.Load() != 0 || runtime.freezeActive.Load() {
			return nil, fmt.Errorf(
				"%w: tree %s still owns active work",
				ErrEngineHasActiveProcesses, rootID,
			)
		}
	}
	done := make(chan struct{})
	e.closeDone = done
	// The Engine owns shutdown independently of any caller's wait. Observation
	// delivery must be joined after releasing the registry lock used by listeners.
	go func() {
		e.observation.close()
		close(done)
	}()
	return done, nil
}

func (e *Engine) reserveProcessStart(
	relation ProcessRelation,
	deploymentRef DeploymentRef,
	treeLimits TreeLimits,
	childRequestDigest Digest,
) error {
	if !relation.Valid() || !deploymentRef.Valid() || !treeLimits.Valid() {
		return ErrInvalidProcessRelation
	}
	reservation := processStartReservation{
		relation:           relation,
		deploymentRef:      deploymentRef,
		treeLimits:         treeLimits,
		childRequestDigest: childRequestDigest,
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	processID := relation.ProcessID()
	if _, exists := e.processes[processID]; exists {
		return ErrProcessAlreadyExists
	}
	if _, exists := e.startReservations[processID]; exists {
		return ErrProcessAlreadyExists
	}
	if e.restoredProcessReserved(processID) {
		return ErrProcessAlreadyExists
	}
	if relation.IsRoot() {
		return e.reserveRootStart(reservation)
	}
	return e.reserveChildStart(reservation)
}

// Hold e.mu so root publication cannot overtake its admission reservation.
func (e *Engine) reserveRootStart(reservation processStartReservation) error {
	if reservation.childRequestDigest.Valid() {
		return ErrInvalidProcessRelation
	}
	e.startReservations[reservation.relation.ProcessID()] = reservation
	return nil
}

// Hold e.mu so child identity and all tree limits are reserved atomically.
func (e *Engine) reserveChildStart(reservation processStartReservation) error {
	relation := reservation.relation
	treeLimits := reservation.treeLimits
	if !reservation.childRequestDigest.Valid() {
		return ErrInvalidProcessRelation
	}
	parentID, child := relation.ParentID()
	key, keyed := relation.ChildKey()
	if !child || !keyed {
		return ErrInvalidProcessRelation
	}
	identity := childIdentity{parent: parentID, key: key}
	if _, exists := e.children[identity]; exists {
		return ErrInvalidChildStart
	}
	if _, exists := e.childStartReservations[identity]; exists {
		return ErrInvalidChildStart
	}
	if e.restoredChildReserved(identity) {
		return ErrInvalidChildStart
	}
	parent := e.processes[parentID]
	if parent == nil {
		return ErrInvalidProcessRelation
	}
	if treeLimits != parent.treeLimits || relation.depth > treeLimits.MaxDepth {
		return ErrResourceLimitExceeded
	}
	childCount, activeChildCount, treeProcessCount := e.reservedTreeCounts(relation.rootID, parentID)
	if childCount >= treeLimits.MaxChildren ||
		activeChildCount >= treeLimits.MaxActiveChildren ||
		treeProcessCount >= treeLimits.MaxTreeProcesses {
		return ErrResourceLimitExceeded
	}
	processID := relation.ProcessID()
	e.startReservations[processID] = reservation
	e.childStartReservations[identity] = processID
	return nil
}

// Hold e.mu so published Processes and pending reservations contribute to one
// consistent limit check.
func (e *Engine) reservedTreeCounts(rootID, parentID ProcessID) (
	childCount uint32,
	activeChildCount uint32,
	treeProcessCount uint32,
) {
	for _, existing := range e.processes {
		if existing.relation.rootID == rootID {
			treeProcessCount++
		}
		if existing.relation.parentID == parentID {
			childCount++
			if !existing.status().Terminal() {
				activeChildCount++
			}
		}
	}
	for _, pending := range e.startReservations {
		if pending.relation.rootID == rootID {
			treeProcessCount++
		}
		if pending.relation.parentID == parentID {
			childCount++
			activeChildCount++
		}
	}
	return childCount, activeChildCount, treeProcessCount
}

func (e *Engine) discardProcessStartReservation(processID ProcessID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reservation, exists := e.startReservations[processID]
	if !exists {
		return
	}
	delete(e.startReservations, processID)
	if parentID, child := reservation.relation.ParentID(); child {
		key, _ := reservation.relation.ChildKey()
		identity := childIdentity{parent: parentID, key: key}
		if e.childStartReservations[identity] == processID {
			delete(e.childStartReservations, identity)
		}
	}
}

func (e *Engine) publishReservedProcess(handle *processHandleState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reservation, exists := e.startReservations[handle.processID]
	if !exists || reservation.relation != handle.relation ||
		reservation.deploymentRef != handle.deploymentRef ||
		reservation.treeLimits != handle.treeLimits || e.closeDone != nil ||
		e.processes[handle.processID] != nil {
		panic("agent: invalid Process start reservation")
	}
	var identity childIdentity
	parentID, isChild := handle.relation.ParentID()
	if isChild {
		key, _ := handle.relation.ChildKey()
		identity = childIdentity{parent: parentID, key: key}
		if e.childStartReservations[identity] != handle.processID ||
			e.children[identity].Valid() {
			panic("agent: invalid child Process start reservation")
		}
	}
	delete(e.startReservations, handle.processID)
	handle.childRequestDigest = reservation.childRequestDigest
	e.processes[handle.processID] = handle
	if handle.relation.IsRoot() {
		if handle.runtime.Load() == nil || e.trees[handle.processID] != nil {
			panic("agent: invalid root tree runtime")
		}
		e.trees[handle.processID] = handle.runtime.Load()
	}
	if isChild {
		parent := e.processes[parentID]
		if parent == nil || handle.runtime.Load() == nil || handle.runtime.Load() != parent.runtime.Load() {
			panic("agent: invalid child tree runtime")
		}
		delete(e.childStartReservations, identity)
		e.children[identity] = handle.processID
	}
}

// InspectTree samples a registered root tree without freezing it or waiting
// for storage acknowledgment. It remains available during a commit, while a
// freeze is acquired or held, and after the owner stops or Engine.Close returns.
// ReleaseTree removes the lookup; an overlapping inspection may return its
// earlier valid sample. ctx bounds both admission and response waiting.
// The owner never calls user execution code to construct a report. Dependencies
// must still return in bounded time: synchronous EventListeners must not query
// or control their own tree. Query failures are returned as errors; runtime
// failures are reported through ProcessInspection.RuntimeError.
func (e *Engine) InspectTree(ctx context.Context, rootID ProcessID) (TreeInspection, error) {
	if e == nil {
		return TreeInspection{}, ErrEngineClosed
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return TreeInspection{}, err
	}
	if err := e.observation.checkListenerReentrancy(ctx, rootID, "InspectTree"); err != nil {
		return TreeInspection{}, err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return TreeInspection{}, err
	}
	return runtime.inspect(ctx)
}

func (e *Engine) acquireTreeOperation(
	ctx context.Context,
	rootID ProcessID,
) (*treeOperation, error) {
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.observation.checkListenerReentrancy(ctx, rootID, "tree operation"); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e.treeOperationsMu.Lock()
		active := e.treeOperations[rootID]
		if active == nil {
			operation := &treeOperation{
				engine: e, rootID: rootID, released: make(chan struct{}),
			}
			e.treeOperations[rootID] = operation
			e.treeOperationsMu.Unlock()
			return operation, nil
		}
		released := active.released
		e.treeOperationsMu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ReleaseTree waits for the complete root tree to settle and removes its
// in-memory registry entries and execution state. It does not cancel work or
// delete Host persistence. Capture any required TreeSnapshot before releasing.
// Existing Process handles retain their Result or RuntimeError;
// Engine.Process and tree operations no longer find the released identities.
// Canceling ctx before settlement leaves the tree registered and usable.
func (e *Engine) ReleaseTree(ctx context.Context, rootID ProcessID) error {
	if e == nil {
		return ErrEngineClosed
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.observation.checkListenerReentrancy(ctx, rootID, "ReleaseTree"); err != nil {
		return err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return err
	}
	select {
	case <-runtime.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Settlement may need another tree operation, so acquire exclusive access
	// only after the runtime has stopped.
	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return err
	}
	defer operation.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.trees[rootID] != runtime {
		return ErrInvalidProcessRelation
	}
	for processID, process := range runtime.processes {
		handle := process.handle
		if parentID, child := handle.relation.ParentID(); child {
			key, _ := handle.relation.ChildKey()
			delete(e.children, childIdentity{parent: parentID, key: key})
		}
		handle.runtime.Store(nil)
		delete(e.processes, processID)
	}
	delete(e.trees, rootID)
	return nil
}

// RestoreTree recreates a complete Process tree from one strict TreeSnapshot.
// rootDeployment must exactly bind the captured root; same-reference children
// reuse it, while other exact references are resolved through EngineConfig's
// DeploymentResolver. Registration is all-or-nothing within this Engine.
// Committed states and nonterminal prepared candidates must restore through
// their exact Definition before registration, activation, or Effect dispatch.
// Interrupted terminal candidates remain inert evidence and are not restored.
// Both completed and prepared completion outputs must satisfy that Definition's
// output schema before admission.
//
// Snapshot capabilities, limits, budgets, and usage remain authoritative. Current
// EngineConfig start defaults do not revoke or rewrite captured grants. The Host
// must authorize the snapshot before calling RestoreTree; ProcessAdmitter and
// initialization acknowledgment are not repeated for captured Processes.
func (e *Engine) RestoreTree(
	ctx context.Context,
	rootDeployment Deployment,
	snapshot TreeSnapshot,
) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = requireContext(ctx)
	if err := rootDeployment.validateDefinition(); err != nil {
		return nil, err
	}
	wire, err := snapshot.wire()
	if err != nil {
		return nil, err
	}
	previousIncarnation, snapshotIsDurable := snapshot.IncarnationID()
	engineIsDurable := e.durability != nil
	if snapshotIsDurable != engineIsDurable {
		return nil, ErrTreeDurabilityMismatch
	}
	rootSnapshot := snapshotByID(wire.ProcessSnapshots, wire.RootID)
	if !rootSnapshot.Valid() || rootSnapshot.DeploymentRef() != rootDeployment.DeploymentRef() {
		return nil, fmt.Errorf("%w: exact root Deployment does not match", ErrInvalidTreeSnapshot)
	}
	operation, err := e.acquireTreeOperation(ctx, wire.RootID)
	if err != nil {
		return nil, err
	}
	defer operation.release()
	previousDigest := snapshot.Digest()
	restoration := treeRestoration{
		engine:      e,
		wire:        wire,
		deployments: map[DeploymentRef]Deployment{rootDeployment.DeploymentRef(): rootDeployment},
	}
	if err := restoration.prepareProcesses(); err != nil {
		return nil, err
	}
	if err := restoration.prepareChildWaits(); err != nil {
		return nil, err
	}
	if err := e.reserveRestoredTree(&restoration); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			e.discardRestoredTree(&restoration)
		}
	}()
	if engineIsDurable {
		incarnation, incarnationErr := newTreeIncarnationID()
		if incarnationErr != nil {
			return nil, incarnationErr
		}
		wire.IncarnationID = &incarnation
		prospectiveSnapshot, snapshotErr := newTreeSnapshot(wire)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		activation, activationErr := newTreeActivation(
			previousIncarnation, previousDigest, incarnation, prospectiveSnapshot,
		)
		if activationErr != nil {
			return nil, activationErr
		}
		if activationErr = activateTree(ctx, e.durability, activation); activationErr != nil {
			return nil, activationErr
		}
		restoration.wire = wire
	}
	restoration.prepareRuntime(ctx)
	e.publishRestoredTree(&restoration)
	published = true
	return e.startRestoredTree(ctx, &restoration), nil
}

func (e *Engine) startRestoredTree(ctx context.Context, restoration *treeRestoration) *Process {
	for index := range restoration.processes {
		entry := &restoration.processes[index]
		if entry.wire.Status.Terminal() {
			entry.handle.publishResult(entry.state.result())
		}
	}
	for index := len(restoration.processes) - 1; index >= 0; index-- {
		entry := &restoration.processes[index]
		if !entry.wire.Status.Terminal() {
			continue
		}
		restoration.runtime.propagateProcessTermination(entry.state)
		restoration.runtime.completeProcessBookkeeping(entry.state)
	}
	root := restoration.runtime.processes[restoration.wire.RootID].handle
	go restoration.runtime.run(requireContext(ctx))
	return &Process{handle: root}
}

func (e *Engine) reserveRestoredTree(restoration *treeRestoration) error {
	if restoration == nil || len(restoration.processes) == 0 {
		return ErrInvalidTreeSnapshot
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	rootID := restoration.wire.RootID
	if e.treeRestoreReservations[rootID] != nil || e.trees[rootID] != nil {
		return ErrProcessAlreadyExists
	}
	for _, process := range restoration.processes {
		if _, exists := e.processes[process.handle.processID]; exists {
			return ErrProcessAlreadyExists
		}
		if _, exists := e.startReservations[process.handle.processID]; exists {
			return ErrProcessAlreadyExists
		}
		if e.restoredProcessReserved(process.handle.processID) {
			return ErrProcessAlreadyExists
		}
		if parentID, child := process.handle.relation.ParentID(); child {
			key, _ := process.handle.relation.ChildKey()
			identity := childIdentity{parent: parentID, key: key}
			if _, exists := e.children[identity]; exists {
				return ErrInvalidChildStart
			}
			if _, exists := e.childStartReservations[identity]; exists {
				return ErrInvalidChildStart
			}
			if e.restoredChildReserved(identity) {
				return ErrInvalidChildStart
			}
		}
	}
	for _, registrations := range restoration.childWaits {
		for _, wait := range registrations {
			if wait == nil || !wait.waitID.Valid() {
				return ErrInvalidChildWait
			}
		}
	}
	e.treeRestoreReservations[rootID] = restoration
	return nil
}

// restoredProcessReserved requires e.mu to be held.
func (e *Engine) restoredProcessReserved(processID ProcessID) bool {
	for _, restoration := range e.treeRestoreReservations {
		for _, process := range restoration.processes {
			if process.handle.processID == processID {
				return true
			}
		}
	}
	return false
}

// restoredChildReserved requires e.mu to be held.
func (e *Engine) restoredChildReserved(identity childIdentity) bool {
	for _, restoration := range e.treeRestoreReservations {
		for _, process := range restoration.processes {
			parentID, child := process.handle.relation.ParentID()
			if !child {
				continue
			}
			key, _ := process.handle.relation.ChildKey()
			if identity == (childIdentity{parent: parentID, key: key}) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) discardRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if restoration != nil && e.treeRestoreReservations[restoration.wire.RootID] == restoration {
		delete(e.treeRestoreReservations, restoration.wire.RootID)
	}
}

func (e *Engine) publishRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rootID := restoration.wire.RootID
	if e.closeDone != nil || e.treeRestoreReservations[rootID] != restoration {
		panic("agent: invalid restored tree reservation")
	}
	runtime := restoration.processes[0].handle.runtime.Load()
	if runtime == nil || runtime.rootID != rootID || e.trees[rootID] != nil {
		panic("agent: invalid restored tree runtime")
	}
	for _, process := range restoration.processes {
		handle := process.handle
		if e.processes[handle.processID] != nil ||
			e.startReservations[handle.processID].relation.Valid() {
			panic("agent: restored Process reservation changed")
		}
		e.processes[handle.processID] = handle
		if parentID, child := handle.relation.ParentID(); child {
			key, _ := handle.relation.ChildKey()
			e.children[childIdentity{parent: parentID, key: key}] = handle.processID
		}
	}
	e.trees[rootID] = runtime
	delete(e.treeRestoreReservations, rootID)
}

// CaptureTree quiesces one complete Engine-owned tree at Strategy-safe
// boundaries and captures a consistent portable cut. In-flight Effects settle
// according to their existing contract before a Process joins the barrier.
// Cancellation remains available while the active work drains.
func (e *Engine) CaptureTree(ctx context.Context, rootID ProcessID) (TreeSnapshot, error) {
	if e == nil {
		return TreeSnapshot{}, ErrInvalidProcessRelation
	}
	ctx = requireContext(ctx)
	if !rootID.Valid() {
		return TreeSnapshot{}, ErrInvalidProcessRelation
	}
	if e.durability != nil {
		return TreeSnapshot{}, ErrTreeCaptureUnavailable
	}
	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return TreeSnapshot{}, err
	}
	defer operation.release()
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return TreeSnapshot{}, err
	}
	freeze, snapshot, err := runtime.acquireTreeFreeze(ctx)
	if err != nil {
		return TreeSnapshot{}, err
	}
	if freeze != nil {
		if releaseErr := freeze.release(); releaseErr != nil {
			return TreeSnapshot{}, releaseErr
		}
	}
	return snapshot, nil
}

func (e *Engine) runtimeForTree(rootID ProcessID) (*treeRuntime, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	root := e.processes[rootID]
	runtime := e.trees[rootID]
	if root == nil || runtime == nil || !root.relation.IsRoot() ||
		root.relation.RootID() != rootID || root.runtime.Load() != runtime {
		return nil, ErrInvalidProcessRelation
	}
	return runtime, nil
}

func newProcessID() (ProcessID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ProcessID{}, fmt.Errorf("agent: generate ProcessID: %w", err)
	}
	return ParseProcessID(processIDPrefix + hex.EncodeToString(random[:]))
}

type processStartReservation struct {
	relation           ProcessRelation
	deploymentRef      DeploymentRef
	treeLimits         TreeLimits
	childRequestDigest Digest
}
