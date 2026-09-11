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

func newProcessID() (ProcessID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ProcessID{}, fmt.Errorf("agent: generate ProcessID: %w", err)
	}
	return ParseProcessID(processIDPrefix + hex.EncodeToString(random[:]))
}
