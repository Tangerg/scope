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

// EngineConfig contains only cross-Strategy execution mechanics. Definition,
// Dispatcher, schema, and behavior configuration belong to each Deployment.
type EngineConfig struct {
	// TreeDurability enables active recovery for complete root Process trees. It
	// owns the atomic Host transaction behind Effect, checkpoint, and
	// activation boundaries. Nil selects zero-configuration ephemeral execution.
	TreeDurability TreeDurability

	// ProcessStartOutcomeAcknowledger enables the optional conclusive handshake
	// after an accepted admission. A started outcome is acknowledged before
	// Process publication; an aborted outcome guarantees no publication.
	ProcessStartOutcomeAcknowledger ProcessStartOutcomeAcknowledger

	// DeploymentResolver supplies exact Deployments requested by child Effects
	// and tree restoration. It is unnecessary for same-Deployment recursion.
	// Resolution is a bounded, context-free local binding lookup only; the
	// resolver does not perform routing or own Process construction or lifecycle.
	DeploymentResolver DeploymentResolver

	// ProcessAdmitter is the optional admission boundary immediately before any
	// root or child Process initializes. It observes immutable Framework facts and
	// may reject, but cannot modify resource or capability allocation.
	ProcessAdmitter ProcessAdmitter

	// EventListeners receive ordered facts for each Process. Different
	// Processes may call a listener concurrently.
	EventListeners []EventListener

	// DeltaListeners receive best-effort streaming increments from the shared
	// bounded queue. Delivery to each listener is sequential.
	DeltaListeners []DeltaListener

	// DeltaBufferCapacity bounds the Engine-wide pending Delta queue. Zero uses
	// the documented internal default; negative values are invalid.
	DeltaBufferCapacity int

	// Limits supplies per-Process execution bounds. Each zero field inherits
	// the corresponding value from DefaultLimits.
	Limits Limits

	// TreeLimits bounds child depth, lifetime fan-out, active children, and the
	// total Process count in each independent tree.
	TreeLimits TreeLimits

	// Capabilities is the maximum authority of each root Process. Child Effects
	// may only allocate subsets and Dispatcher Effects declare what they require.
	Capabilities CapabilitySet
}

// Engine is the sole owner of Process construction, scheduling, lifecycle,
// Signal delivery, Effect dispatch, and snapshot boundaries. It contains no
// Deployment catalog or Host persistence abstraction. Engine values must be
// constructed with NewEngine and must not be copied after first use.
type Engine struct {
	durability               TreeDurability
	startOutcomeAcknowledger ProcessStartOutcomeAcknowledger
	resolver                 DeploymentResolver
	admitter                 ProcessAdmitter
	observation              *observationBus
	limits                   Limits
	treeLimits               TreeLimits
	capabilities             CapabilitySet

	// Tree-wide administrative operations use an independent serial lane so a
	// caller waiting for one root never holds the registry lock needed by Engine
	// lifecycle paths. The two locks are therefore never nested.
	treeOperationsMu sync.Mutex
	treeOperations   map[ProcessID]*treeOperation

	// mu protects only the process/tree registry and admission reservations;
	// each treeRuntime owns execution state after publication.
	mu                      sync.RWMutex
	processes               map[ProcessID]*processController
	trees                   map[ProcessID]*treeRuntime
	startReservations       map[ProcessID]processStartReservation
	treeRestoreReservations map[ProcessID]*treeRestoration
	children                map[childIdentity]ProcessID
	childStartReservations  map[childIdentity]ProcessID
	// A non-nil closeDone closes admission; receiving from it joins closure.
	closeDone chan struct{}
}

// ObservationFailures returns a concurrency-safe snapshot of listener panics
// isolated by this Engine. The counts do not alter Process state or Usage.
func (e *Engine) ObservationFailures() ObservationFailureCounts {
	if e == nil || e.observation == nil {
		return ObservationFailureCounts{}
	}
	return e.observation.failureCounts()
}

// FlushDeltas waits until every best-effort Delta accepted before this call has
// finished delivery to the configured listeners. Deltas rejected by the bounded
// queue remain dropped; the method is an ordering barrier, not a reliability
// upgrade. Callers use it before publishing a final value that must not overtake
// its already-accepted streaming observations.
func (e *Engine) FlushDeltas(ctx context.Context) error {
	if e == nil {
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
	if config.ProcessStartOutcomeAcknowledger != nil && lo.IsNil(config.ProcessStartOutcomeAcknowledger) {
		return nil, fmt.Errorf("%w: ProcessStartOutcomeAcknowledger is typed nil", ErrInvalidEngineConfig)
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
		durability:               config.TreeDurability,
		startOutcomeAcknowledger: config.ProcessStartOutcomeAcknowledger,
		resolver:                 config.DeploymentResolver,
		admitter:                 config.ProcessAdmitter,
		observation:              newObservationBus(config.EventListeners, config.DeltaListeners, capacity),
		limits:                   limits,
		treeLimits:               treeLimits,
		capabilities:             config.Capabilities,
		treeOperations:           make(map[ProcessID]*treeOperation),
		processes:                make(map[ProcessID]*processController),
		trees:                    make(map[ProcessID]*treeRuntime),
		startReservations:        make(map[ProcessID]processStartReservation),
		treeRestoreReservations:  make(map[ProcessID]*treeRestoration),
		children:                 make(map[childIdentity]ProcessID),
		childStartReservations:   make(map[childIdentity]ProcessID),
	}, nil
}

type childIdentity struct {
	parent ProcessID
	key    ChildKey
}

// Start validates Input, creates exactly one Execution, registers its Process,
// and starts the Engine-owned loop. Canceling ctx records a Host cancellation
// or deadline; use a longer-lived context for execution beyond a request.
func (e *Engine) Start(ctx context.Context, deployment Deployment, input Input) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = requireContext(ctx)
	if !deployment.Valid() {
		return nil, ErrInvalidDeployment
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
	if requestProcessAdmissionErr := requestProcessAdmission(ctx, e.admitter, admission); requestProcessAdmissionErr != nil {
		e.discardProcessStartReservation(id)
		return nil, requestProcessAdmissionErr
	}
	startedAt := time.Now().Round(0).UTC()
	execution, state, failure, err := initializeExecution(deployment.Definition(), input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessStartOutcome(ctx, e.startOutcomeAcknowledger, abortedProcessOutcome(admission, failure))
		e.discardProcessStartReservation(id)
		return nil, errors.Join(fmt.Errorf("agent: initialize Process: %w", err), acknowledgeErr)
	}
	if err := acknowledgeProcessStartOutcome(ctx, e.startOutcomeAcknowledger, startedProcessOutcome(admission, startedAt)); err != nil {
		e.discardProcessStartReservation(id)
		return nil, err
	}
	controller := newProcessController(
		relation, deployment.DeploymentRef(), budget, e.capabilities,
		e.treeLimits,
		startedAt, StatusRunning,
	)
	loop := newProcessState(e, controller, deployment, execution, state, startedAt, e.limits)
	runtime := newTreeRuntime(e, relation.RootID(), ctx, loop)
	if e.durability != nil {
		incarnation, incarnationErr := newTreeIncarnationID()
		if incarnationErr != nil {
			e.discardProcessStartReservation(id)
			return nil, incarnationErr
		}
		runtime.incarnation = incarnation
		baseSnapshot, captureErr := runtime.captureTree()
		if captureErr != nil {
			e.discardProcessStartReservation(id)
			return nil, captureErr
		}
		checkpoint, err := newTreeCheckpoint(TreeCheckpointStart, Digest{}, baseSnapshot)
		if err != nil {
			e.discardProcessStartReservation(id)
			return nil, err
		}
		if err := commitTreeCheckpoint(ctx, e.durability, checkpoint); err != nil {
			e.discardProcessStartReservation(id)
			return nil, err
		}
		runtime.establishDurableHead(incarnation, baseSnapshot)
	}
	e.publishReservedProcess(controller)
	go runtime.run(ctx)
	return &Process{controller: controller}, nil
}

// Run starts one Process and waits for its terminal result. Once Start succeeds,
// Run waits for safe finalization even if ctx is canceled; the same ctx has
// already recorded the Process termination intent.
func (e *Engine) Run(ctx context.Context, deployment Deployment, input Input) (Result, error) {
	process, err := e.Start(ctx, deployment, input)
	if err != nil {
		return Result{}, err
	}
	return process.Await(context.WithoutCancel(requireContext(ctx)))
}

// Process returns an Engine-issued handle for an identity known to this Engine.
func (e *Engine) Process(id ProcessID) (*Process, bool) {
	if e == nil || !id.Valid() {
		return nil, false
	}
	e.mu.RLock()
	controller, exists := e.processes[id]
	e.mu.RUnlock()
	if !exists {
		return nil, false
	}
	return &Process{controller: controller}, true
}

// Close releases observation workers after all Process instances have completed
// or stopped and their owned work has settled. Concurrent calls wait for the
// same completed closure. Results and RuntimeErrors remain readable from
// existing handles.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closeDone != nil {
		done := e.closeDone
		e.mu.Unlock()
		<-done
		return nil
	}
	if len(e.startReservations) != 0 || len(e.treeRestoreReservations) != 0 {
		e.mu.Unlock()
		return fmt.Errorf("%w: Process publication is pending", ErrEngineHasActiveProcesses)
	}
	var unpublished []*processController
	for _, controller := range e.processes {
		select {
		case <-controller.done:
		default:
			if !controller.status().Terminal() {
				e.mu.Unlock()
				return fmt.Errorf(
					"%w: Process %s is still running",
					ErrEngineHasActiveProcesses, controller.processID,
				)
			}
		}
		select {
		case <-controller.treeSettled:
		default:
			unpublished = append(unpublished, controller)
		}
	}
	for rootID, runtime := range e.trees {
		if runtime.inflight.Load() != 0 || runtime.freezeHeld.Load() {
			e.mu.Unlock()
			return fmt.Errorf(
				"%w: tree %s still owns active work",
				ErrEngineHasActiveProcesses, rootID,
			)
		}
	}
	if len(unpublished) != 0 && e.durability != nil {
		e.mu.Unlock()
		return fmt.Errorf(
			"%w: Process %s has unpublished durable tree state",
			ErrEngineHasActiveProcesses, unpublished[0].processID,
		)
	}
	done := make(chan struct{})
	e.closeDone = done
	e.mu.Unlock()
	// Admission is closed before joining publication, whose listeners may
	// inspect the registry and therefore need e.mu to remain available.
	for _, controller := range unpublished {
		<-controller.treeSettled
	}
	e.observation.close()
	close(done)
	return nil
}

func newProcessID() (ProcessID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ProcessID{}, fmt.Errorf("agent: generate ProcessID: %w", err)
	}
	return ParseProcessID(processIDPrefix + hex.EncodeToString(random[:]))
}
