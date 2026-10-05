package guardian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
)

type Config struct {
	DataDir             string
	Reads               platform.ReadSet
	WriteRuntime        platform.WriteRuntime
	ActiveActions       []domain.Action
	PlatformProfile     string
	ReadSource          string
	Clock               func() time.Time
	ApprovalTTL         time.Duration
	HistoryRetention    time.Duration
	StepDelay           time.Duration
	MaxConcurrentRuns   int
	EvidenceStepMinutes float64
	Model               agentkit.ModelConfig
	BriefGenerator      agentkit.BriefGenerator
}

type RunCoordinator interface {
	AcquireRun(
		context.Context,
		domain.RunID,
	) (context.Context, func() error, error)
}

type runAvailabilityClassifier interface {
	IsRunUnavailable(error) bool
}

type DurableConfig struct {
	Config
	Journal     audit.Journal
	Effects     idempotency.Executor
	History     history.ConversationPersistenceAdapter
	Coordinator RunCoordinator
}

type RunView struct {
	RunID      domain.RunID      `json:"run_id"`
	IncidentID domain.IncidentID `json:"incident_id"`
	WaybillID  domain.WaybillID  `json:"waybill_id"`
	Status     domain.RunStatus  `json:"status"`
	LastSeq    audit.Seq         `json:"last_seq"`
}

type DecisionRequest struct {
	Kind         approval.DecisionKind
	DecidedBy    string
	RejectReason string
}

type runStartedPayload struct {
	IncidentID domain.IncidentID             `json:"incident_id"`
	WaybillID  domain.WaybillID              `json:"waybill_id"`
	Status     domain.RunStatus              `json:"status"`
	Profile    string                        `json:"platform_profile,omitempty"`
	ReadSource string                        `json:"read_source,omitempty"`
	Inference  *agentkit.InferenceDescriptor `json:"inference,omitempty"`
}

type proposalPreparedPayload struct {
	ProposalID   string            `json:"proposal_id"`
	ApprovalID   domain.ApprovalID `json:"approval_id"`
	SDKRunID     string            `json:"sdk_run_id"`
	PlanVersion  int               `json:"plan_version"`
	Proposal     proposal.Accepted `json:"proposal"`
	Writes       []approval.Item   `json:"writes"`
	WritesDigest string            `json:"writes_digest"`
	RequestedAt  time.Time         `json:"requested_at,omitempty"`
	ExpiresAt    time.Time         `json:"expires_at"`
	legacyID     bool
}

var (
	ErrServiceClosed              = errors.New("guardian service is closed")
	ErrRecoverySourceUnavailable  = errors.New("effect recovery source is unavailable")
	ErrRecoveryReadSourceMismatch = errors.New(
		"run recovery read source does not match the configured source",
	)
	ErrRunCapacity = errors.New("concurrent run capacity reached")
)

type Service struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	clock               func() time.Time
	ttl                 time.Duration
	evidenceStepMinutes float64

	reads           platform.ReadSet
	platformProfile string
	readSource      string
	journal         audit.Journal
	approvals       *approval.Store
	effects         idempotency.Executor
	registry        *guardtools.Registry
	engine          *agentkit.Engine
	briefGenerator  agentkit.BriefGenerator
	briefCache      *briefCache
	briefSource     string
	coordinator     RunCoordinator
	recovery        audit.RecoveryJournal

	mu        sync.Mutex
	runs      map[domain.RunID]RunView
	locks     map[domain.RunID]*sync.Mutex
	timers    map[domain.ApprovalID]chan struct{}
	runSlots  chan struct{}
	closed    bool
	closeDone chan struct{}
	closeErr  error
	wg        sync.WaitGroup
}

func Open(config Config) (*Service, error) {
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	journal, err := audit.Open(config.DataDir, config.Clock)
	if err != nil {
		return nil, err
	}
	writeRuntime := config.WriteRuntime
	if writeRuntime == nil {
		writeRuntime, err = guardtools.NewFixtureWriteRuntime(config.Reads)
		if err != nil {
			return nil, errors.Join(err, journal.Close())
		}
	}
	idempotencyStore, err := idempotency.NewStore(journal, idempotency.StoreConfig{
		Runtime: writeRuntime,
		Clock:   config.Clock,
	})
	if err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	return openService(config, journal, idempotencyStore, nil, nil)
}

func OpenDurable(config DurableConfig) (*Service, error) {
	config.Config = normalizeConfig(config.Config)
	if err := validateConfig(config.Config); err != nil {
		return nil, err
	}
	if config.Journal == nil ||
		config.Effects == nil ||
		config.History == nil ||
		config.Coordinator == nil {
		return nil, fmt.Errorf("durable journal, effects, history, and coordinator are required")
	}
	return openService(
		config.Config,
		config.Journal,
		config.Effects,
		config.History,
		config.Coordinator,
	)
}

func openService(
	config Config,
	journal audit.Journal,
	effects idempotency.Executor,
	persistence history.ConversationPersistenceAdapter,
	coordinator RunCoordinator,
) (*Service, error) {
	closeJournal := func(err error) (*Service, error) {
		return nil, errors.Join(err, journal.Close())
	}
	recovery, _ := journal.(audit.RecoveryJournal)
	if recovery != nil {
		if err := recovery.PrepareRecovery(context.Background()); err != nil {
			return closeJournal(err)
		}
	}
	approvals, err := approval.NewStore(journal, config.Clock)
	if err != nil {
		return closeJournal(err)
	}
	handlers, err := guardtools.NewHandlers(config.Reads)
	if err != nil {
		return closeJournal(err)
	}
	var registry *guardtools.Registry
	if config.ActiveActions == nil {
		registry, err = guardtools.NewRegistry(handlers)
	} else {
		registry, err = guardtools.NewRegistryForActions(handlers, config.ActiveActions)
	}
	if err != nil {
		return closeJournal(err)
	}
	briefGenerator := config.BriefGenerator
	if briefGenerator == nil {
		briefGenerator, err = agentkit.NewBriefGenerator(config.Model)
		if err != nil {
			return closeJournal(err)
		}
	}
	middlewares := []agents.Middleware{
		agentkit.NewAuditMiddleware(journal),
		agentkit.NewReadBindingMiddleware(config.Reads),
		agentkit.NewWriteEffectMiddleware(approvals, effects, registry),
	}
	var engine *agentkit.Engine
	if persistence == nil {
		engine, err = agentkit.NewEngine(
			filepath.Join(config.DataDir, "hastekit"),
			registry,
			middlewares,
			config.StepDelay,
			config.Model,
			agentkit.HistoryPolicy{
				Retention: config.HistoryRetention,
				Clock:     config.Clock,
			},
		)
	} else {
		engine, err = agentkit.NewEngineWithPersistence(
			persistence,
			registry,
			middlewares,
			config.StepDelay,
			config.Model,
		)
	}
	if err != nil {
		return closeJournal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		ctx:                 ctx,
		cancel:              cancel,
		clock:               config.Clock,
		ttl:                 config.ApprovalTTL,
		evidenceStepMinutes: config.EvidenceStepMinutes,
		reads:               config.Reads,
		platformProfile:     config.PlatformProfile,
		readSource:          config.ReadSource,
		journal:             journal,
		approvals:           approvals,
		effects:             effects,
		registry:            registry,
		engine:              engine,
		briefGenerator:      briefGenerator,
		briefCache:          newBriefCache(ctx, config.Clock),
		briefSource:         briefModelSource(config.Model.Model),
		coordinator:         coordinator,
		recovery:            recovery,
		runs:                make(map[domain.RunID]RunView),
		locks:               make(map[domain.RunID]*sync.Mutex),
		timers:              make(map[domain.ApprovalID]chan struct{}),
		runSlots:            make(chan struct{}, config.MaxConcurrentRuns),
		closeDone:           make(chan struct{}),
	}
	if err := service.rebuildRuns(); err != nil {
		cancel()
		return nil, errors.Join(err, engine.Close(), journal.Close())
	}
	return service, nil
}

func normalizeConfig(config Config) Config {
	if config.DataDir == "" {
		config.DataDir = "data"
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.ApprovalTTL <= 0 {
		config.ApprovalTTL = 10 * time.Minute
	}
	if config.HistoryRetention == 0 {
		config.HistoryRetention = 7 * 24 * time.Hour
	}
	if config.MaxConcurrentRuns == 0 {
		config.MaxConcurrentRuns = 8
	}
	if config.EvidenceStepMinutes == 0 {
		config.EvidenceStepMinutes = 8
	}
	return config
}

func validateConfig(config Config) error {
	if config.HistoryRetention < 0 {
		return fmt.Errorf("history retention must be positive")
	}
	if config.MaxConcurrentRuns < 0 {
		return fmt.Errorf("maximum concurrent runs must be positive")
	}
	if config.EvidenceStepMinutes < 0 {
		return fmt.Errorf("evidence step minutes must be positive")
	}
	if config.Reads.TMS == nil ||
		config.Reads.Weather == nil ||
		config.Reads.Catalog == nil {
		return fmt.Errorf("all platform readers are required")
	}
	return nil
}

func (s *Service) StartRun(
	ctx context.Context,
	waybillID domain.WaybillID,
) (RunView, error) {
	if err := s.beginOperation(); err != nil {
		return RunView{}, err
	}
	defer s.wg.Done()
	if err := ctx.Err(); err != nil {
		return RunView{}, err
	}
	if err := domain.ValidateWaybillID(waybillID); err != nil {
		return RunView{}, err
	}
	if _, err := s.reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: waybillID,
	}); err != nil {
		return RunView{}, err
	}
	if !s.acquireRunSlot() {
		return RunView{}, ErrRunCapacity
	}
	slotOwned := true
	defer func() {
		if slotOwned {
			s.releaseRunSlot()
		}
	}()

	runID := domain.RunID(uuid.NewString())
	run := RunView{
		RunID:      runID,
		IncidentID: domain.IncidentID("incident-" + string(runID)),
		WaybillID:  waybillID,
		Status:     domain.RunStarted,
	}
	inference := s.engine.Inference()
	event, err := s.journal.Append(ctx, runID, audit.Draft{
		EventID: "run:" + string(runID) + ":started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunStarted,
		Payload: runStartedPayload{
			IncidentID: run.IncidentID,
			WaybillID:  run.WaybillID,
			Status:     run.Status,
			Profile:    s.platformProfile,
			ReadSource: s.readSource,
			Inference:  &inference,
		},
	})
	if err != nil {
		return RunView{}, err
	}
	run.LastSeq = event.Seq
	s.setRun(run)

	s.wg.Add(1)
	slotOwned = false
	go func() {
		defer s.wg.Done()
		defer s.releaseRunSlot()
		lock := s.lockFor(runID)
		lock.Lock()
		defer lock.Unlock()
		runCtx, release, err := s.acquireRun(s.ctx, runID)
		if err != nil {
			return
		}
		defer func() {
			_ = release()
		}()
		s.updateRunStatus(runID, domain.RunInvestigating)
		outcome, err := s.engine.Start(runCtx, domain.RunContext{
			RunID:       run.RunID,
			IncidentID:  run.IncidentID,
			WaybillID:   run.WaybillID,
			PlanVersion: 1,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
				_ = s.recordRunErrorWithContext(runCtx, runID, err)
			}
			return
		}
		if err := s.handleOutcome(runCtx, run, 1, outcome, false); err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
				_ = s.recordRunErrorWithContext(runCtx, runID, err)
			}
		}
	}()
	return run, nil
}

func (s *Service) acquireRunSlot() bool {
	select {
	case s.runSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseRunSlot() {
	<-s.runSlots
}

func (s *Service) Decide(
	ctx context.Context,
	id domain.ApprovalID,
	request DecisionRequest,
) (approval.Approval, error) {
	if err := s.beginOperation(); err != nil {
		return approval.Approval{}, err
	}
	defer s.wg.Done()

	current, err := s.approvals.Get(id)
	if err != nil {
		return approval.Approval{}, err
	}
	lock := s.lockFor(current.RunID)
	lock.Lock()
	defer lock.Unlock()
	runCtx, release, err := s.acquireRun(s.ctx, current.RunID)
	if err != nil {
		return approval.Approval{}, err
	}
	defer func() {
		_ = release()
	}()

	current, err = s.approvals.Get(id)
	if err != nil {
		return approval.Approval{}, err
	}
	decided, err := s.approvals.Decide(runCtx, id, approval.Decision{
		Kind:         request.Kind,
		DecidedBy:    request.DecidedBy,
		RejectReason: request.RejectReason,
	})
	if err != nil {
		if errors.Is(err, approval.ErrDecisionConflict) && decided.Status == approval.StatusExpired {
			s.cancelExpiration(id)
			if _, resumeErr := s.resumeApproval(runCtx, decided, false); resumeErr != nil {
				var recordErr error
				if !errors.Is(resumeErr, context.Canceled) &&
					!errors.Is(resumeErr, agentkit.ErrEngineClosed) {
					recordErr = s.recordRunErrorWithContext(runCtx, decided.RunID, resumeErr)
				}
				return approval.Approval{}, errors.Join(err, resumeErr, recordErr)
			}
		}
		return approval.Approval{}, err
	}
	s.cancelExpiration(id)
	if decided.Status == approval.StatusExecuted {
		return decided, nil
	}
	if current.Status != approval.StatusPending &&
		!((current.Status == approval.StatusConfirmed ||
			current.Status == approval.StatusReconciliationRequired) &&
			request.Kind == approval.DecisionConfirm) {
		return decided, nil
	}

	approved := decided.Status == approval.StatusConfirmed
	runStatus := s.run(current.RunID).Status
	if approved && isTerminal(runStatus) {
		if runStatus == domain.RunCompleted && s.approvalEffectsSucceeded(decided) {
			return s.approvals.MarkExecuted(ctx, id)
		}
		return approval.Approval{}, fmt.Errorf("cannot resume approval for terminal run %q", runStatus)
	}
	result, err := s.resumeApproval(runCtx, decided, approved)
	if err != nil {
		var recordErr error
		if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
			recordErr = s.recordRunErrorWithContext(runCtx, decided.RunID, err)
		}
		return approval.Approval{}, errors.Join(err, recordErr)
	}
	return result, nil
}

func (s *Service) Timeline(
	ctx context.Context,
	runID domain.RunID,
	after audit.Seq,
) (*audit.Subscription, error) {
	return s.journal.Subscribe(ctx, runID, after)
}

func (s *Service) Replay(ctx context.Context, runID domain.RunID, after audit.Seq) ([]audit.Event, error) {
	return s.journal.Replay(ctx, runID, after)
}

func (s *Service) GetRun(runID domain.RunID) (RunView, error) {
	if s.recovery != nil {
		projection, err := s.recovery.RunProjection(context.Background(), runID)
		if err != nil {
			return RunView{}, err
		}
		run := RunView{
			RunID:      projection.RunID,
			IncidentID: projection.IncidentID,
			WaybillID:  projection.WaybillID,
			Status:     projection.Status,
			LastSeq:    projection.LastSeq,
		}
		s.setRun(run)
		return run, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.runs[runID]
	if !ok {
		return RunView{}, audit.ErrRunNotFound
	}
	return value, nil
}

func (s *Service) GetApproval(id domain.ApprovalID) (approval.Approval, error) {
	return s.approvals.Get(id)
}

func (s *Service) CurrentApproval(runID domain.RunID) (approval.Approval, error) {
	values := s.approvals.List()
	sort.Slice(values, func(i, j int) bool {
		return values[i].RequestedAt.After(values[j].RequestedAt)
	})
	for _, value := range values {
		if value.RunID == runID &&
			(value.Status == approval.StatusPending ||
				value.Status == approval.StatusConfirmed ||
				value.Status == approval.StatusReconciliationRequired) {
			return value, nil
		}
	}
	return approval.Approval{}, approval.ErrNotFound
}

func (s *Service) Recover(ctx context.Context) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.wg.Done()
	if err := s.validateRecoveryReadSources(ctx); err != nil {
		return err
	}
	if err := s.recoverPreparedApprovals(ctx); err != nil {
		return err
	}

	values := s.approvals.List()
	latestPlan := make(map[domain.RunID]int)
	runsWithApproval := make(map[domain.RunID]bool)
	for _, value := range values {
		runsWithApproval[value.RunID] = true
		if value.PlanVersion > latestPlan[value.RunID] {
			latestPlan[value.RunID] = value.PlanVersion
		}
	}

	for _, value := range values {
		switch value.Status {
		case approval.StatusPending:
			if value.ExpiresAt.After(s.clock().UTC()) {
				s.scheduleExpiration(value)
				continue
			}
			if err := s.expireApproval(ctx, value.ID); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				if recordErr := s.recordRunError(value.RunID, err); recordErr != nil {
					return errors.Join(err, recordErr)
				}
			}
		case approval.StatusConfirmed, approval.StatusReconciliationRequired:
			if err := s.recoverConfirmedApproval(ctx, value); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				if recordErr := s.recordRunError(value.RunID, err); recordErr != nil {
					return errors.Join(err, recordErr)
				}
			}
		case approval.StatusRejected, approval.StatusExpired:
			if value.PlanVersion < latestPlan[value.RunID] || isTerminal(s.run(value.RunID).Status) {
				continue
			}
			if err := s.recoverDecision(value, false); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				if recordErr := s.recordRunError(value.RunID, err); recordErr != nil {
					return errors.Join(err, recordErr)
				}
			}
		}
	}

	for _, run := range s.runSnapshot() {
		if runsWithApproval[run.RunID] || isTerminal(run.Status) {
			continue
		}
		lock := s.lockFor(run.RunID)
		lock.Lock()
		usesProposalProtocol, err := s.runUsesProposalProtocol(ctx, run.RunID)
		if err != nil {
			lock.Unlock()
			return err
		}
		if usesProposalProtocol {
			if err := s.recordReviewRequired(
				run.RunID,
				errors.Join(
					proposal.ErrReviewRequired,
					errors.New("run stopped before a durable proposal checkpoint"),
				),
			); err != nil {
				lock.Unlock()
				if s.isRunUnavailable(err) {
					continue
				}
				return err
			}
		} else {
			if err := s.recordFailure(
				run.RunID,
				errors.New("run stopped before a durable approval checkpoint"),
			); err != nil {
				lock.Unlock()
				if s.isRunUnavailable(err) {
					continue
				}
				return err
			}
		}
		lock.Unlock()
	}
	return nil
}

func (s *Service) recoverPreparedApprovals(ctx context.Context) error {
	for _, run := range s.runSnapshot() {
		if isTerminal(run.Status) {
			continue
		}
		events, err := s.journal.Replay(ctx, run.RunID, 0)
		if err != nil {
			return err
		}
		prepared, ok, err := latestPreparedProposal(events)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := s.approvals.Get(prepared.ApprovalID); err == nil {
			continue
		} else if !errors.Is(err, approval.ErrNotFound) {
			return err
		}

		lock := s.lockFor(run.RunID)
		lock.Lock()
		runCtx, release, err := s.acquireRun(s.ctx, run.RunID)
		if err != nil {
			lock.Unlock()
			if s.isRunUnavailable(err) {
				continue
			}
			return err
		}
		_, materializeErr := s.materializePreparedProposal(runCtx, run, prepared)
		if materializeErr != nil {
			if errors.Is(materializeErr, proposal.ErrReviewRequired) {
				recordErr := s.recordReviewRequiredWithContext(runCtx, run.RunID, materializeErr)
				releaseErr := release()
				lock.Unlock()
				if err := errors.Join(recordErr, releaseErr); err != nil {
					return err
				}
				continue
			}
			releaseErr := release()
			lock.Unlock()
			return errors.Join(materializeErr, releaseErr)
		}
		s.updateRunStatus(run.RunID, domain.RunAwaitingApproval)
		releaseErr := release()
		lock.Unlock()
		if releaseErr != nil {
			return releaseErr
		}
	}
	return nil
}

func latestPreparedProposal(events []audit.Event) (proposalPreparedPayload, bool, error) {
	var latest proposalPreparedPayload
	found := false
	for _, event := range events {
		if event.Type != audit.EventProposalPrepared {
			continue
		}
		var prepared proposalPreparedPayload
		if err := json.Unmarshal(event.Payload, &prepared); err != nil {
			return proposalPreparedPayload{}, false, fmt.Errorf(
				"decode proposal checkpoint %q: %w",
				event.EventID,
				err,
			)
		}
		if prepared.RequestedAt.IsZero() {
			prepared.RequestedAt = event.TS.UTC()
			prepared.legacyID = true
		} else {
			prepared.RequestedAt = prepared.RequestedAt.UTC()
		}
		prepared.ExpiresAt = prepared.ExpiresAt.UTC()
		if !found || prepared.PlanVersion >= latest.PlanVersion {
			latest = prepared
			found = true
		}
	}
	return latest, found, nil
}

func (s *Service) runUsesProposalProtocol(
	ctx context.Context,
	runID domain.RunID,
) (bool, error) {
	events, err := s.journal.Replay(ctx, runID, 0)
	if err != nil {
		return false, err
	}
	if len(events) == 0 || events[0].Type != audit.EventRunStarted {
		return false, fmt.Errorf("run %q has no run_started prefix", runID)
	}
	var payload runStartedPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		return false, fmt.Errorf("decode run %q inference: %w", runID, err)
	}
	return payload.Inference != nil, nil
}

func (s *Service) validateRecoveryReadSources(ctx context.Context) error {
	for _, run := range s.runSnapshot() {
		if isTerminal(run.Status) {
			continue
		}
		events, err := s.journal.Replay(ctx, run.RunID, 0)
		if err != nil {
			return err
		}
		if len(events) == 0 || events[0].Type != audit.EventRunStarted {
			return fmt.Errorf("run %q has no run_started prefix", run.RunID)
		}
		var payload runStartedPayload
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			return fmt.Errorf("decode run %q source: %w", run.RunID, err)
		}
		if payload.ReadSource != s.readSource {
			return fmt.Errorf(
				"%w: run %q uses %q, configured %q",
				ErrRecoveryReadSourceMismatch,
				run.RunID,
				payload.ReadSource,
				s.readSource,
			)
		}
	}
	return nil
}

func (s *Service) RunEffectReconciler(
	ctx context.Context,
	pollInterval time.Duration,
) error {
	if pollInterval <= 0 {
		return fmt.Errorf("effect reconciliation poll interval must be positive")
	}
	if _, ok := s.effects.(idempotency.RecoverySource); !ok {
		return ErrRecoverySourceUnavailable
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.reconcileDueEffects(ctx); err != nil {
				if ctx.Err() != nil || errors.Is(err, ErrServiceClosed) {
					return nil
				}
				return err
			}
		}
	}
}

func (s *Service) reconcileDueEffects(ctx context.Context) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.wg.Done()

	source, ok := s.effects.(idempotency.RecoverySource)
	if !ok {
		return ErrRecoverySourceUnavailable
	}
	commands, err := source.DueRecoveries(ctx)
	if err != nil {
		return err
	}
	if len(commands) == 0 {
		return nil
	}

	approvalsByEffect := make(map[domain.EffectID]approval.Approval)
	for _, value := range s.approvals.List() {
		if value.Status != approval.StatusConfirmed &&
			value.Status != approval.StatusReconciliationRequired {
			continue
		}
		for _, item := range value.Items {
			approvalsByEffect[item.EffectID] = value
		}
	}
	seen := make(map[domain.ApprovalID]struct{})
	for _, command := range commands {
		value, exists := approvalsByEffect[command.Identity.EffectID]
		if !exists {
			continue
		}
		if value.RunID != command.RunID {
			return fmt.Errorf(
				"due effect %q run %q does not match approval run %q",
				command.Identity.EffectID,
				command.RunID,
				value.RunID,
			)
		}
		if _, exists := seen[value.ID]; exists {
			continue
		}
		seen[value.ID] = struct{}{}
		if err := s.recoverConfirmedApproval(ctx, value); err != nil {
			if s.isRunUnavailable(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Service) isRunUnavailable(err error) bool {
	classifier, ok := s.coordinator.(runAvailabilityClassifier)
	return ok && classifier.IsRunUnavailable(err)
}

func (s *Service) recoverConfirmedApproval(
	ctx context.Context,
	value approval.Approval,
) error {
	reconciliationPending, err := s.reconcileApprovalEffects(ctx, value)
	if err != nil {
		return err
	}
	if reconciliationPending {
		return nil
	}
	value, err = s.approvals.Get(value.ID)
	if err != nil {
		return err
	}
	run := s.run(value.RunID)
	if isTerminal(run.Status) {
		if run.Status == domain.RunCompleted && s.approvalEffectsSucceeded(value) {
			_, err = s.approvals.MarkExecuted(ctx, value.ID)
			return err
		}
		if run.Status == domain.RunCompleted {
			return fmt.Errorf("completed run %q has incomplete approval effects", run.RunID)
		}
		return nil
	}
	return s.recoverDecision(value, true)
}

func (s *Service) recoverDecision(value approval.Approval, approved bool) error {
	lock := s.lockFor(value.RunID)
	lock.Lock()
	defer lock.Unlock()
	runCtx, release, err := s.acquireRun(s.ctx, value.RunID)
	if err != nil {
		return err
	}
	defer func() {
		_ = release()
	}()

	current, err := s.approvals.Get(value.ID)
	if err != nil {
		return err
	}
	if !approved {
		if current.Status != approval.StatusRejected && current.Status != approval.StatusExpired {
			return nil
		}
	} else if current.Status != approval.StatusConfirmed &&
		current.Status != approval.StatusReconciliationRequired {
		return nil
	}
	if isTerminal(s.run(value.RunID).Status) {
		if approved && s.run(value.RunID).Status == domain.RunCompleted &&
			s.approvalEffectsSucceeded(current) {
			_, err = s.approvals.MarkExecuted(runCtx, current.ID)
		}
		return err
	}
	if _, err := s.resumeApproval(runCtx, current, approved); err != nil {
		if !errors.Is(err, context.Canceled) {
			return errors.Join(err, s.recordRunErrorWithContext(runCtx, current.RunID, err))
		}
		return err
	}
	return nil
}

func (s *Service) resumeApproval(
	ctx context.Context,
	value approval.Approval,
	approved bool,
) (approval.Approval, error) {
	planVersion := value.PlanVersion
	if !approved {
		planVersion++
	}
	s.updateRunStatus(value.RunID, domain.RunExecuting)
	outcome, err := s.engine.Resume(ctx, domain.RunContext{
		RunID:       value.RunID,
		IncidentID:  s.run(value.RunID).IncidentID,
		WaybillID:   value.WaybillID,
		PlanVersion: planVersion,
	}, value.SDKRunID, interruptsFromApproval(value), approved)
	if err != nil {
		if approved {
			results, allSucceeded := s.approvalEffectResults(value)
			if executionRequiresReconciliation(results) {
				pending, markErr := s.approvals.MarkReconciliationRequired(ctx, value.ID, results)
				if markErr != nil {
					return approval.Approval{}, errors.Join(err, markErr)
				}
				s.updateRunStatus(value.RunID, domain.RunExecuting)
				return pending, nil
			}
			if !allSucceeded {
				failed, markErr := s.approvals.MarkExecutionFailed(ctx, value.ID, results)
				if markErr != nil {
					return approval.Approval{}, errors.Join(err, markErr)
				}
				return failed, s.recordFailureWithContext(ctx, value.RunID, err)
			}
		}
		return approval.Approval{}, err
	}
	if approved {
		results, allSucceeded := s.approvalEffectResults(value)
		if !allSucceeded {
			if executionRequiresReconciliation(results) {
				pending, markErr := s.approvals.MarkReconciliationRequired(ctx, value.ID, results)
				if markErr != nil {
					return approval.Approval{}, markErr
				}
				s.updateRunStatus(value.RunID, domain.RunExecuting)
				return pending, nil
			}
			failed, markErr := s.approvals.MarkExecutionFailed(ctx, value.ID, results)
			if markErr != nil {
				return approval.Approval{}, markErr
			}
			recordErr := s.recordFailureWithContext(
				ctx,
				value.RunID,
				fmt.Errorf("approval %q has incomplete effects", value.ID),
			)
			return failed, recordErr
		}
	}
	if err := s.handleOutcome(ctx, s.run(value.RunID), planVersion, outcome, !approved); err != nil {
		return approval.Approval{}, err
	}
	if approved {
		return s.approvals.MarkExecuted(ctx, value.ID)
	}
	return value, nil
}

func (s *Service) expireApproval(ctx context.Context, id domain.ApprovalID) error {
	current, err := s.approvals.Get(id)
	if err != nil {
		return err
	}
	lock := s.lockFor(current.RunID)
	lock.Lock()
	defer lock.Unlock()
	runCtx, release, err := s.acquireRun(s.ctx, current.RunID)
	if err != nil {
		return err
	}
	defer func() {
		_ = release()
	}()

	current, err = s.approvals.Get(id)
	if err != nil {
		return err
	}
	if current.Status != approval.StatusPending {
		return nil
	}
	expired, err := s.approvals.Decide(runCtx, id, approval.Decision{Kind: approval.DecisionExpire})
	if err != nil {
		return err
	}
	s.cancelExpiration(id)
	if _, err := s.resumeApproval(runCtx, expired, false); err != nil {
		return err
	}
	return nil
}

func (s *Service) reconcileApprovalEffects(
	ctx context.Context,
	value approval.Approval,
) (bool, error) {
	runCtx, release, err := s.acquireRun(ctx, value.RunID)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = release()
	}()
	recoveryPending := false
	for _, item := range value.Items {
		identity, err := item.Identity()
		if err != nil {
			return false, err
		}
		command := idempotency.Command{
			RunID:    value.RunID,
			CallID:   item.CallID,
			Identity: identity,
		}
		outcome, err := s.effects.Recover(runCtx, command)
		if err != nil {
			return false, err
		}
		switch outcome.Decision {
		case idempotency.RecoveryBusy, idempotency.RecoveryPending,
			idempotency.RecoveryManualReview:
			recoveryPending = true
			continue
		}
	}
	results, _ := s.approvalEffectResults(value)
	if !recoveryPending && !executionRequiresReconciliation(results) {
		return false, nil
	}
	if _, err := s.approvals.MarkReconciliationRequired(runCtx, value.ID, results); err != nil {
		return false, err
	}
	s.updateRunStatus(value.RunID, domain.RunExecuting)
	return true, nil
}

func (s *Service) scheduleExpiration(value approval.Approval) {
	if value.Status != approval.StatusPending {
		return
	}
	cancel := make(chan struct{})
	s.mu.Lock()
	if s.closed || s.timers[value.ID] != nil {
		s.mu.Unlock()
		return
	}
	s.timers[value.ID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	delay := value.ExpiresAt.Sub(s.clock().UTC())
	if delay < 0 {
		delay = 0
	}
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			s.removeExpiration(value.ID, cancel)
			if err := s.expireApproval(s.ctx, value.ID); err != nil &&
				!errors.Is(err, context.Canceled) {
				_ = s.recordRunError(value.RunID, err)
			}
		case <-cancel:
		case <-s.ctx.Done():
		}
	}()
}

func (s *Service) cancelExpiration(id domain.ApprovalID) {
	s.mu.Lock()
	cancel := s.timers[id]
	delete(s.timers, id)
	s.mu.Unlock()
	if cancel != nil {
		close(cancel)
	}
}

func (s *Service) removeExpiration(id domain.ApprovalID, cancel chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timers[id] == cancel {
		delete(s.timers, id)
	}
}

func (s *Service) approvalEffectsSucceeded(value approval.Approval) bool {
	_, allSucceeded := s.approvalEffectResults(value)
	return allSucceeded
}

func (s *Service) approvalEffectResults(value approval.Approval) ([]approval.ItemExecution, bool) {
	if len(value.Items) == 0 {
		return nil, false
	}
	results := make([]approval.ItemExecution, 0, len(value.Items))
	allSucceeded := true
	for _, item := range value.Items {
		identity, err := item.Identity()
		if err != nil {
			allSucceeded = false
			results = append(results, approval.ItemExecution{
				CallID:         item.CallID,
				Action:         item.Action,
				EffectID:       item.EffectID,
				IdempotencyKey: item.IdempotencyKey,
				Status:         approval.ExecutionMissing,
			})
			continue
		}
		state, ok := s.effects.Status(idempotency.Command{
			RunID:    value.RunID,
			CallID:   item.CallID,
			Identity: identity,
		})
		status := approval.ExecutionMissing
		if ok {
			switch state {
			case idempotency.StateSucceeded:
				status = approval.ExecutionSucceeded
			case idempotency.StateRetryableFailed:
				status = approval.ExecutionRetryable
			case idempotency.StatePermanentFailed:
				status = approval.ExecutionPermanent
			case idempotency.StateStarted:
				status = approval.ExecutionStarted
			case idempotency.StateUnknown:
				status = approval.ExecutionUnknown
			case idempotency.StateReconciling:
				status = approval.ExecutionReconciling
			case idempotency.StateManualReview:
				status = approval.ExecutionManualReview
			}
		}
		if status != approval.ExecutionSucceeded {
			allSucceeded = false
		}
		results = append(results, approval.ItemExecution{
			CallID:         item.CallID,
			Action:         item.Action,
			EffectID:       item.EffectID,
			IdempotencyKey: item.IdempotencyKey,
			Status:         status,
		})
	}
	return results, allSucceeded
}

func executionRequiresReconciliation(results []approval.ItemExecution) bool {
	for _, result := range results {
		switch result.Status {
		case approval.ExecutionStarted,
			approval.ExecutionIndeterminate,
			approval.ExecutionUnknown,
			approval.ExecutionReconciling,
			approval.ExecutionManualReview:
			return true
		}
	}
	return false
}

func (s *Service) runSnapshot() []RunView {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]RunView, 0, len(s.runs))
	for _, run := range s.runs {
		result = append(result, run)
	}
	return result
}

func isTerminal(status domain.RunStatus) bool {
	return status == domain.RunCompleted ||
		status == domain.RunRejected ||
		status == domain.RunFailed ||
		status == domain.RunReviewRequired ||
		status == domain.RunManualReview
}

func (s *Service) beginOperation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrServiceClosed
	}
	s.wg.Add(1)
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return s.closeErr
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	s.briefCache.wait()
	closeErr := errors.Join(s.engine.Close(), s.journal.Close())
	s.mu.Lock()
	s.closeErr = closeErr
	close(s.closeDone)
	s.mu.Unlock()
	return closeErr
}

func (s *Service) handleOutcome(
	ctx context.Context,
	run RunView,
	planVersion int,
	outcome agentkit.Outcome,
	afterReject bool,
) error {
	switch outcome.Status {
	case agentstate.RunStatusPaused:
		if len(outcome.Interrupts) == 0 {
			return fmt.Errorf("agent paused without interrupts")
		}
		prepared, err := s.prepareProposal(ctx, run, planVersion, outcome)
		if err != nil {
			return err
		}
		if _, err := s.materializePreparedProposal(ctx, run, prepared); err != nil {
			return err
		}
		s.updateRunStatus(run.RunID, domain.RunAwaitingApproval)
	case agentstate.RunStatusCompleted:
		eventType := audit.EventRunCompleted
		status := domain.RunCompleted
		if afterReject {
			eventType = audit.EventRunRejected
			status = domain.RunRejected
		}
		event, err := s.journal.Append(ctx, run.RunID, audit.Draft{
			EventID: "run:" + string(run.RunID) + ":" + string(status),
			Actor:   audit.ActorAgent,
			Type:    eventType,
			Payload: map[string]any{"status": status, "summary": outcome.Text},
		})
		if err != nil {
			return err
		}
		run.Status = status
		run.LastSeq = event.Seq
		s.setRun(run)
	default:
		return fmt.Errorf("agent ended with status %q", outcome.Status)
	}
	return nil
}

func (s *Service) prepareProposal(
	ctx context.Context,
	run RunView,
	planVersion int,
	outcome agentkit.Outcome,
) (proposalPreparedPayload, error) {
	if outcome.SDKRunID == "" || outcome.Proposal == nil {
		return proposalPreparedPayload{}, errors.Join(
			proposal.ErrReviewRequired,
			errors.New("paused agent run has no accepted proposal"),
		)
	}
	compiler, err := proposal.NewCompiler(s.journal)
	if err != nil {
		return proposalPreparedPayload{}, err
	}
	if err := compiler.Verify(ctx, run.RunID, *outcome.Proposal); err != nil {
		var validationErr *proposal.ValidationError
		if errors.As(err, &validationErr) {
			return proposalPreparedPayload{}, errors.Join(proposal.ErrReviewRequired, err)
		}
		return proposalPreparedPayload{}, err
	}
	items, err := s.approvalItems(run, planVersion, outcome.Interrupts)
	if err != nil {
		return proposalPreparedPayload{}, errors.Join(proposal.ErrReviewRequired, err)
	}
	callIDs := make([]string, 0, len(items))
	for _, item := range items {
		callIDs = append(callIDs, item.CallID)
	}
	approvalID := approval.IDForPlan(run.RunID, planVersion, callIDs)
	writesDigest, err := approvalItemsDigest(items)
	if err != nil {
		return proposalPreparedPayload{}, err
	}
	requestedAt := s.clock().UTC()
	prepared := proposalPreparedPayload{
		ProposalID:   proposalIDFor(approvalID),
		ApprovalID:   approvalID,
		SDKRunID:     outcome.SDKRunID,
		PlanVersion:  planVersion,
		Proposal:     *outcome.Proposal,
		Writes:       items,
		WritesDigest: writesDigest,
		RequestedAt:  requestedAt,
		ExpiresAt:    requestedAt.Add(s.ttl),
	}
	eventID := proposalPreparedEventID(run.RunID, planVersion)
	event, err := s.journal.Append(ctx, run.RunID, audit.Draft{
		EventID: eventID,
		Actor:   audit.ActorAgent,
		Type:    audit.EventProposalPrepared,
		Payload: prepared,
	})
	if err != nil {
		return proposalPreparedPayload{}, err
	}
	var stored proposalPreparedPayload
	if err := json.Unmarshal(event.Payload, &stored); err != nil {
		return proposalPreparedPayload{}, fmt.Errorf("decode prepared proposal: %w", err)
	}
	if err := s.validatePreparedProposal(ctx, run, stored); err != nil {
		return proposalPreparedPayload{}, err
	}
	if !samePreparedProposal(prepared, stored) {
		return proposalPreparedPayload{}, errors.Join(
			proposal.ErrReviewRequired,
			errors.New("proposal checkpoint conflicts with the paused agent outcome"),
		)
	}
	return stored, nil
}

func (s *Service) materializePreparedProposal(
	ctx context.Context,
	run RunView,
	prepared proposalPreparedPayload,
) (approval.Approval, error) {
	if err := s.validatePreparedProposal(ctx, run, prepared); err != nil {
		return approval.Approval{}, err
	}
	evidence := proposalEvidence(prepared.Proposal)
	if _, err := s.journal.Append(ctx, run.RunID, audit.Draft{
		EventID: fmt.Sprintf("run:%s:attribution:%d", run.RunID, prepared.PlanVersion),
		Actor:   audit.ActorAgent,
		Type:    audit.EventAttribution,
		Payload: map[string]any{
			"summary":         prepared.Proposal.Summary,
			"confidence_bps":  prepared.Proposal.ConfidenceBPS,
			"attribution":     prepared.Proposal.Attribution,
			"evidence":        evidence,
			"alternatives":    prepared.Proposal.Alternatives,
			"expected_impact": prepared.Proposal.ExpectedImpact,
			"proposal_digest": prepared.Proposal.Digest,
			"plan_version":    prepared.PlanVersion,
		},
	}); err != nil {
		return approval.Approval{}, err
	}
	value := approval.Approval{
		ID:          prepared.ApprovalID,
		RunID:       run.RunID,
		SDKRunID:    prepared.SDKRunID,
		WaybillID:   run.WaybillID,
		PlanVersion: prepared.PlanVersion,
		Items:       prepared.Writes,
		Reason:      prepared.Proposal.Summary,
		Evidence:    evidence,
		ProposalRef: &approval.ProposalRef{
			ProposalID: prepared.ProposalID,
			EventID:    proposalPreparedEventID(run.RunID, prepared.PlanVersion),
			Digest:     prepared.Proposal.Digest,
		},
		RequestedAt: prepared.RequestedAt,
		ExpiresAt:   prepared.ExpiresAt,
	}
	created, err := s.approvals.Create(ctx, value)
	if err != nil {
		return approval.Approval{}, err
	}
	s.scheduleExpiration(created)
	return created, nil
}

func (s *Service) approvalItems(
	run RunView,
	planVersion int,
	interrupts []agentkit.Interrupt,
) ([]approval.Item, error) {
	items := make([]approval.Item, 0, len(interrupts))
	callIDs := make(map[string]struct{}, len(interrupts))
	runContext := domain.RunContext{
		RunID:       run.RunID,
		IncidentID:  run.IncidentID,
		WaybillID:   run.WaybillID,
		PlanVersion: planVersion,
	}
	for _, interrupt := range interrupts {
		if interrupt.CallID == "" {
			return nil, errors.New("write interrupt has no call_id")
		}
		if _, exists := callIDs[interrupt.CallID]; exists {
			return nil, fmt.Errorf("write interrupts contain duplicate call_id %q", interrupt.CallID)
		}
		callIDs[interrupt.CallID] = struct{}{}
		write, err := s.registry.ParseActiveWrite(interrupt.WireName, interrupt.Arguments)
		if err != nil {
			return nil, err
		}
		if write.Action != interrupt.Action {
			return nil, fmt.Errorf(
				"interrupt action %q does not match tool action %q",
				interrupt.Action,
				write.Action,
			)
		}
		if err := write.ValidateRunContext(runContext); err != nil {
			return nil, err
		}
		if write.LegacyKey != "" {
			return nil, fmt.Errorf(
				"%w: model supplied idempotency_key",
				idempotency.ErrInvalidIdentity,
			)
		}
		identity, err := idempotency.Derive(idempotency.DerivationInput{
			RunContext: runContext,
			Action:     write.Action,
			Target:     write.Target,
			Arguments:  write.Arguments,
		})
		if err != nil {
			return nil, err
		}
		items = append(items, approval.Item{
			CallID:          interrupt.CallID,
			Action:          write.Action,
			WireName:        write.WireName,
			Params:          write.Arguments,
			ArgumentsHash:   identity.ArgumentsHash,
			IdentityVersion: identity.Version,
			EffectID:        identity.EffectID,
			IdempotencyKey:  identity.Key,
		})
	}
	return items, nil
}

func (s *Service) validatePreparedProposal(
	ctx context.Context,
	run RunView,
	prepared proposalPreparedPayload,
) error {
	if prepared.SDKRunID == "" ||
		prepared.PlanVersion <= 0 ||
		prepared.ApprovalID == "" ||
		prepared.ProposalID == "" ||
		len(prepared.Writes) == 0 ||
		prepared.RequestedAt.IsZero() ||
		prepared.ExpiresAt.IsZero() {
		return errors.Join(
			proposal.ErrReviewRequired,
			errors.New("proposal checkpoint is incomplete"),
		)
	}
	if !prepared.ExpiresAt.After(prepared.RequestedAt) {
		return errors.Join(
			proposal.ErrReviewRequired,
			errors.New("proposal checkpoint expires_at must be after requested_at"),
		)
	}
	callIDs := make([]string, 0, len(prepared.Writes))
	interrupts := make([]agentkit.Interrupt, 0, len(prepared.Writes))
	for _, item := range prepared.Writes {
		callIDs = append(callIDs, item.CallID)
		interrupts = append(interrupts, agentkit.Interrupt{
			CallID:    item.CallID,
			WireName:  item.WireName,
			Action:    item.Action,
			Arguments: append(json.RawMessage(nil), item.Params...),
		})
	}
	expectedApprovalID := approval.IDForPlan(run.RunID, prepared.PlanVersion, callIDs)
	if prepared.legacyID {
		expectedApprovalID = approval.IDFor(run.RunID, callIDs)
	}
	if prepared.ApprovalID != expectedApprovalID ||
		prepared.ProposalID != proposalIDFor(expectedApprovalID) {
		return errors.Join(
			proposal.ErrReviewRequired,
			errors.New("proposal checkpoint identity does not match its writes"),
		)
	}
	canonicalItems, err := s.approvalItems(run, prepared.PlanVersion, interrupts)
	if err != nil {
		return errors.Join(proposal.ErrReviewRequired, err)
	}
	for index := range canonicalItems {
		if !sameApprovalItem(prepared.Writes[index], canonicalItems[index]) {
			return errors.Join(
				proposal.ErrReviewRequired,
				fmt.Errorf("proposal checkpoint write %d is not canonical", index),
			)
		}
	}
	expectedWritesDigest, err := approvalItemsDigest(prepared.Writes)
	if err != nil {
		return err
	}
	if prepared.WritesDigest != expectedWritesDigest {
		return errors.Join(
			proposal.ErrReviewRequired,
			errors.New("proposal checkpoint writes digest does not match"),
		)
	}
	compiler, err := proposal.NewCompiler(s.journal)
	if err != nil {
		return err
	}
	if err := compiler.Verify(ctx, run.RunID, prepared.Proposal); err != nil {
		var validationErr *proposal.ValidationError
		if errors.As(err, &validationErr) {
			return errors.Join(proposal.ErrReviewRequired, err)
		}
		return err
	}
	return nil
}

func proposalEvidence(accepted proposal.Accepted) []approval.Evidence {
	var evidence []approval.Evidence
	for _, attribution := range accepted.Attribution {
		for _, citation := range attribution.Evidence {
			evidence = append(evidence, approval.Evidence{
				Label: attribution.Factor,
				Value: citation.DisplayValue,
				Source: &approval.EvidenceSource{
					ToolCallID: citation.ToolCallID,
					FieldPath:  string(citation.FieldPath),
					SourceSeq:  citation.SourceSeq,
				},
			})
		}
	}
	return evidence
}

func approvalItemsDigest(items []approval.Item) (string, error) {
	type digestItem struct {
		CallID          string                      `json:"call_id"`
		Action          domain.Action               `json:"action"`
		WireName        string                      `json:"wire_name"`
		ArgumentsHash   string                      `json:"arguments_hash"`
		IdentityVersion idempotency.IdentityVersion `json:"identity_version,omitempty"`
		EffectID        domain.EffectID             `json:"effect_id,omitempty"`
		IdempotencyKey  domain.IdempotencyKey       `json:"idempotency_key"`
	}
	values := make([]digestItem, 0, len(items))
	for _, item := range items {
		values = append(values, digestItem{
			CallID:          item.CallID,
			Action:          item.Action,
			WireName:        item.WireName,
			ArgumentsHash:   item.ArgumentsHash,
			IdentityVersion: item.IdentityVersion,
			EffectID:        item.EffectID,
			IdempotencyKey:  item.IdempotencyKey,
		})
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal approval writes: %w", err)
	}
	sum := sha256.Sum256(append([]byte("waybill-proposal-writes-v1\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func sameApprovalItem(actual, expected approval.Item) bool {
	return actual.CallID == expected.CallID &&
		actual.Action == expected.Action &&
		actual.WireName == expected.WireName &&
		actual.ArgumentsHash == expected.ArgumentsHash &&
		actual.IdentityVersion == expected.IdentityVersion &&
		actual.EffectID == expected.EffectID &&
		actual.IdempotencyKey == expected.IdempotencyKey
}

func samePreparedProposal(actual, expected proposalPreparedPayload) bool {
	return actual.ProposalID == expected.ProposalID &&
		actual.ApprovalID == expected.ApprovalID &&
		actual.SDKRunID == expected.SDKRunID &&
		actual.PlanVersion == expected.PlanVersion &&
		actual.Proposal.Digest == expected.Proposal.Digest &&
		actual.WritesDigest == expected.WritesDigest &&
		actual.RequestedAt.Equal(expected.RequestedAt) &&
		actual.ExpiresAt.Equal(expected.ExpiresAt)
}

func proposalPreparedEventID(runID domain.RunID, planVersion int) string {
	return fmt.Sprintf("run:%s:proposal:%d:prepared", runID, planVersion)
}

func proposalIDFor(approvalID domain.ApprovalID) string {
	return "proposal:" + string(approvalID)
}

func (s *Service) rebuildRuns() error {
	if s.recovery != nil {
		projections, err := s.recovery.RunProjections(context.Background())
		if err != nil {
			return err
		}
		for _, run := range projections {
			s.runs[run.RunID] = RunView{
				RunID:      run.RunID,
				IncidentID: run.IncidentID,
				WaybillID:  run.WaybillID,
				Status:     run.Status,
				LastSeq:    run.LastSeq,
			}
		}
		return nil
	}
	events, err := s.journal.AllEvents(context.Background())
	if err != nil {
		return err
	}
	projected, err := projectRuns(events)
	if err != nil {
		return err
	}
	for runID, run := range projected {
		s.runs[runID] = RunView{
			RunID:      run.RunID,
			IncidentID: run.IncidentID,
			WaybillID:  run.WaybillID,
			Status:     run.Status,
			LastSeq:    run.LastSeq,
		}
	}
	return nil
}

func (s *Service) recordFailure(runID domain.RunID, cause error) error {
	ctx, release, err := s.acquireRun(context.Background(), runID)
	if err != nil {
		return err
	}
	recordErr := s.recordFailureWithContext(ctx, runID, cause)
	return errors.Join(recordErr, release())
}

func (s *Service) recordRunError(runID domain.RunID, cause error) error {
	ctx, release, err := s.acquireRun(context.Background(), runID)
	if err != nil {
		return err
	}
	recordErr := s.recordRunErrorWithContext(ctx, runID, cause)
	return errors.Join(recordErr, release())
}

func (s *Service) recordReviewRequired(runID domain.RunID, cause error) error {
	ctx, release, err := s.acquireRun(context.Background(), runID)
	if err != nil {
		return err
	}
	recordErr := s.recordReviewRequiredWithContext(ctx, runID, cause)
	return errors.Join(recordErr, release())
}

func (s *Service) recordRunErrorWithContext(
	ctx context.Context,
	runID domain.RunID,
	cause error,
) error {
	if errors.Is(cause, proposal.ErrReviewRequired) {
		return s.recordReviewRequiredWithContext(ctx, runID, cause)
	}
	return s.recordFailureWithContext(ctx, runID, cause)
}

func (s *Service) recordReviewRequiredWithContext(
	ctx context.Context,
	runID domain.RunID,
	cause error,
) error {
	payload := map[string]any{
		"status": domain.RunReviewRequired,
		"error":  cause.Error(),
	}
	var validationErr *proposal.ValidationError
	if errors.As(cause, &validationErr) {
		payload["issue_code"] = validationErr.Code
	}
	event, err := s.journal.Append(ctx, runID, audit.Draft{
		EventID: "run:" + string(runID) + ":review_required",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunReviewRequired,
		Payload: payload,
	})
	if err != nil {
		return err
	}
	run := s.run(runID)
	run.Status = domain.RunReviewRequired
	run.LastSeq = event.Seq
	s.setRun(run)
	return nil
}

func (s *Service) recordFailureWithContext(
	ctx context.Context,
	runID domain.RunID,
	cause error,
) error {
	event, err := s.journal.Append(ctx, runID, audit.Draft{
		EventID: "run:" + string(runID) + ":failed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunFailed,
		Payload: map[string]any{"status": domain.RunFailed, "error": cause.Error()},
	})
	if err != nil {
		return err
	}
	run := s.run(runID)
	run.Status = domain.RunFailed
	run.LastSeq = event.Seq
	s.setRun(run)
	return nil
}

func (s *Service) acquireRun(
	ctx context.Context,
	runID domain.RunID,
) (context.Context, func() error, error) {
	if s.coordinator == nil {
		return ctx, func() error { return nil }, nil
	}
	return s.coordinator.AcquireRun(ctx, runID)
}

func (s *Service) updateRunStatus(runID domain.RunID, status domain.RunStatus) {
	run := s.run(runID)
	run.Status = status
	s.setRun(run)
}

func (s *Service) run(runID domain.RunID) RunView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[runID]
}

func (s *Service) setRun(run RunView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = run
}

func (s *Service) lockFor(runID domain.RunID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[runID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[runID] = lock
	}
	return lock
}

func interruptsFromApproval(value approval.Approval) []agentkit.Interrupt {
	result := make([]agentkit.Interrupt, 0, len(value.Items))
	for _, item := range value.Items {
		result = append(result, agentkit.Interrupt{
			CallID:   item.CallID,
			WireName: item.WireName,
			Action:   item.Action,
		})
	}
	return result
}

func IsNotFound(err error) bool {
	return errors.Is(err, audit.ErrRunNotFound) || errors.Is(err, approval.ErrNotFound)
}
