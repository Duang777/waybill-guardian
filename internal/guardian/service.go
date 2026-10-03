package guardian

import (
	"context"
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
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
)

type Config struct {
	DataDir     string
	Clients     platform.Clients
	Clock       func() time.Time
	ApprovalTTL time.Duration
	StepDelay   time.Duration
	Model       agentkit.ModelConfig
}

type RunView struct {
	RunID      domain.RunID      `json:"run_id"`
	IncidentID domain.IncidentID `json:"incident_id"`
	WaybillID  domain.WaybillID  `json:"waybill_id"`
	Status     domain.RunStatus  `json:"status"`
	LastSeq    audit.Seq         `json:"last_seq"`
}

type RiskScore struct {
	ETADelay int `json:"eta_delay"`
	Road     int `json:"road"`
	Weather  int `json:"weather"`
}

type WaybillView struct {
	Waybill  platform.Waybill       `json:"waybill"`
	Tracking []platform.TrackPoint  `json:"tracking"`
	Driver   platform.Driver        `json:"driver"`
	Weather  []platform.RoadWeather `json:"weather"`
	Risk     RiskScore              `json:"risk"`
}

type DecisionRequest struct {
	Kind         approval.DecisionKind
	DecidedBy    string
	RejectReason string
}

type runStartedPayload struct {
	IncidentID domain.IncidentID `json:"incident_id"`
	WaybillID  domain.WaybillID  `json:"waybill_id"`
	Status     domain.RunStatus  `json:"status"`
}

var ErrServiceClosed = errors.New("guardian service is closed")

type Service struct {
	ctx    context.Context
	cancel context.CancelFunc
	clock  func() time.Time
	ttl    time.Duration

	clients   platform.Clients
	journal   audit.Journal
	approvals *approval.Store
	effects   *idempotency.Store
	registry  *guardtools.Registry
	engine    *agentkit.Engine

	mu        sync.Mutex
	runs      map[domain.RunID]RunView
	locks     map[domain.RunID]*sync.Mutex
	timers    map[domain.ApprovalID]chan struct{}
	closed    bool
	closeDone chan struct{}
	closeErr  error
	wg        sync.WaitGroup
}

func Open(config Config) (*Service, error) {
	if config.DataDir == "" {
		config.DataDir = "data"
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.ApprovalTTL <= 0 {
		config.ApprovalTTL = 10 * time.Minute
	}
	if config.Clients.TMS == nil || config.Clients.Weather == nil || config.Clients.Notification == nil {
		return nil, fmt.Errorf("all platform clients are required")
	}
	journal, err := audit.Open(config.DataDir, config.Clock)
	if err != nil {
		return nil, err
	}
	closeJournal := func(err error) (*Service, error) {
		return nil, errors.Join(err, journal.Close())
	}
	approvals, err := approval.NewStore(journal, config.Clock)
	if err != nil {
		return closeJournal(err)
	}
	idempotencyStore, err := idempotency.NewStore(journal, platformEffectLookup(config.Clients))
	if err != nil {
		return closeJournal(err)
	}
	handlers, err := guardtools.NewHandlers(config.Clients)
	if err != nil {
		return closeJournal(err)
	}
	registry, err := guardtools.NewRegistry(handlers)
	if err != nil {
		return closeJournal(err)
	}
	middlewares := []agents.Middleware{
		agentkit.NewAuditMiddleware(journal),
		agentkit.NewWriteEffectMiddleware(approvals, idempotencyStore, registry),
	}
	engine, err := agentkit.NewEngine(
		filepath.Join(config.DataDir, "hastekit"),
		registry,
		middlewares,
		config.StepDelay,
		config.Model,
	)
	if err != nil {
		return closeJournal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		ctx:       ctx,
		cancel:    cancel,
		clock:     config.Clock,
		ttl:       config.ApprovalTTL,
		clients:   config.Clients,
		journal:   journal,
		approvals: approvals,
		effects:   idempotencyStore,
		registry:  registry,
		engine:    engine,
		runs:      make(map[domain.RunID]RunView),
		locks:     make(map[domain.RunID]*sync.Mutex),
		timers:    make(map[domain.ApprovalID]chan struct{}),
		closeDone: make(chan struct{}),
	}
	if err := service.rebuildRuns(); err != nil {
		cancel()
		return nil, errors.Join(err, engine.Close(), journal.Close())
	}
	return service, nil
}

func platformEffectLookup(clients platform.Clients) idempotency.LookupFunc {
	return func(ctx context.Context, command idempotency.Command) (platform.EffectResult, error) {
		request := platform.LookupEffectRequest{
			Action:         command.Identity.Action,
			IdempotencyKey: command.Identity.Key,
		}
		switch command.Identity.Action {
		case domain.ActionReassign, domain.ActionCreateClaim:
			return clients.TMS.LookupEffect(ctx, request)
		case domain.ActionSendSMS:
			return clients.Notification.LookupEffect(ctx, request)
		default:
			return platform.EffectResult{Disposition: platform.EffectPermanentFailed}, nil
		}
	}
}

func (s *Service) StartDemo(ctx context.Context) (RunView, error) {
	if err := s.beginOperation(); err != nil {
		return RunView{}, err
	}
	defer s.wg.Done()

	runID := domain.RunID(uuid.NewString())
	run := RunView{
		RunID:      runID,
		IncidentID: domain.IncidentID("delay-" + string(runID)),
		WaybillID:  "YD2026101001",
		Status:     domain.RunStarted,
	}
	event, err := s.journal.Append(ctx, runID, audit.Draft{
		EventID: "run:" + string(runID) + ":started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunStarted,
		Payload: runStartedPayload{
			IncidentID: run.IncidentID,
			WaybillID:  run.WaybillID,
			Status:     run.Status,
		},
	})
	if err != nil {
		return RunView{}, err
	}
	run.LastSeq = event.Seq
	s.setRun(run)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		lock := s.lockFor(runID)
		lock.Lock()
		defer lock.Unlock()
		s.updateRunStatus(runID, domain.RunInvestigating)
		outcome, err := s.engine.Start(s.ctx, domain.RunContext{
			RunID:       run.RunID,
			IncidentID:  run.IncidentID,
			WaybillID:   run.WaybillID,
			PlanVersion: 1,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
				s.recordFailure(runID, err)
			}
			return
		}
		if err := s.handleOutcome(s.ctx, run, 1, outcome, false); err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
				s.recordFailure(runID, err)
			}
		}
	}()
	return run, nil
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

	current, err = s.approvals.Get(id)
	if err != nil {
		return approval.Approval{}, err
	}
	decided, err := s.approvals.Decide(ctx, id, approval.Decision{
		Kind:         request.Kind,
		DecidedBy:    request.DecidedBy,
		RejectReason: request.RejectReason,
	})
	if err != nil {
		if errors.Is(err, approval.ErrDecisionConflict) && decided.Status == approval.StatusExpired {
			s.cancelExpiration(id)
			if _, resumeErr := s.resumeApproval(s.ctx, decided, false); resumeErr != nil {
				if !errors.Is(resumeErr, context.Canceled) &&
					!errors.Is(resumeErr, agentkit.ErrEngineClosed) {
					s.recordFailure(decided.RunID, resumeErr)
				}
				return approval.Approval{}, errors.Join(err, resumeErr)
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
	result, err := s.resumeApproval(s.ctx, decided, approved)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, agentkit.ErrEngineClosed) {
			s.recordFailure(decided.RunID, err)
		}
		return approval.Approval{}, err
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

func (s *Service) GetWaybill(ctx context.Context, id domain.WaybillID) (WaybillView, error) {
	if err := domain.ValidateWaybillID(id); err != nil {
		return WaybillView{}, err
	}
	waybill, err := s.clients.TMS.GetWaybill(ctx, platform.GetWaybillRequest{WaybillID: id})
	if err != nil {
		return WaybillView{}, err
	}
	tracking, err := s.clients.TMS.GetTracking(ctx, platform.GetTrackingRequest{WaybillID: id})
	if err != nil {
		return WaybillView{}, err
	}
	driver, err := s.clients.TMS.GetDriver(ctx, platform.GetDriverRequest{DriverID: waybill.DriverID})
	if err != nil {
		return WaybillView{}, err
	}
	weather, err := s.clients.Weather.GetRoadWeather(ctx, platform.GetRoadWeatherRequest{
		Route: waybill.Origin + "-" + waybill.Destination,
	})
	if err != nil {
		return WaybillView{}, err
	}
	waybill.ShipperPhone = audit.MaskPhone(waybill.ShipperPhone)
	driver.Phone = audit.MaskPhone(driver.Phone)
	driver.Plate = audit.MaskPlate(driver.Plate)
	return WaybillView{
		Waybill:  waybill,
		Tracking: tracking,
		Driver:   driver,
		Weather:  weather,
		Risk:     RiskScore{ETADelay: 86, Road: 34, Weather: 8},
	}, nil
}

func (s *Service) Recover(ctx context.Context) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.wg.Done()

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
				s.recordFailure(value.RunID, err)
			}
		case approval.StatusConfirmed, approval.StatusReconciliationRequired:
			reconciliationPending, err := s.reconcileApprovalEffects(ctx, value)
			if err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				s.recordFailure(value.RunID, err)
				continue
			}
			if reconciliationPending {
				continue
			}
			value, err = s.approvals.Get(value.ID)
			if err != nil {
				s.recordFailure(value.RunID, err)
				continue
			}
			run := s.run(value.RunID)
			if isTerminal(run.Status) {
				if run.Status == domain.RunCompleted && s.approvalEffectsSucceeded(value) {
					if _, err := s.approvals.MarkExecuted(ctx, value.ID); err != nil {
						s.recordFailure(value.RunID, err)
					}
				} else if run.Status == domain.RunCompleted {
					s.recordFailure(
						value.RunID,
						fmt.Errorf("completed run %q has incomplete approval effects", run.RunID),
					)
				}
				continue
			}
			if err := s.recoverDecision(value, true); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				s.recordFailure(value.RunID, err)
			}
		case approval.StatusRejected, approval.StatusExpired:
			if value.PlanVersion < latestPlan[value.RunID] || isTerminal(s.run(value.RunID).Status) {
				continue
			}
			if err := s.recoverDecision(value, false); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				s.recordFailure(value.RunID, err)
			}
		}
	}

	for _, run := range s.runSnapshot() {
		if runsWithApproval[run.RunID] || isTerminal(run.Status) {
			continue
		}
		lock := s.lockFor(run.RunID)
		lock.Lock()
		s.recordFailure(run.RunID, errors.New("run stopped before a durable approval checkpoint"))
		lock.Unlock()
	}
	return nil
}

func (s *Service) recoverDecision(value approval.Approval, approved bool) error {
	lock := s.lockFor(value.RunID)
	lock.Lock()
	defer lock.Unlock()

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
			_, err = s.approvals.MarkExecuted(s.ctx, current.ID)
		}
		return err
	}
	if _, err := s.resumeApproval(s.ctx, current, approved); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.recordFailure(current.RunID, err)
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
			results, _ := s.approvalEffectResults(value)
			if executionRequiresReconciliation(results) {
				pending, markErr := s.approvals.MarkReconciliationRequired(ctx, value.ID, results)
				if markErr != nil {
					return approval.Approval{}, errors.Join(err, markErr)
				}
				s.updateRunStatus(value.RunID, domain.RunExecuting)
				return pending, nil
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
			s.recordFailure(value.RunID, fmt.Errorf("approval %q has incomplete effects", value.ID))
			return failed, nil
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

	current, err = s.approvals.Get(id)
	if err != nil {
		return err
	}
	if current.Status != approval.StatusPending {
		return nil
	}
	expired, err := s.approvals.Decide(ctx, id, approval.Decision{Kind: approval.DecisionExpire})
	if err != nil {
		return err
	}
	s.cancelExpiration(id)
	if _, err := s.resumeApproval(s.ctx, expired, false); err != nil {
		return err
	}
	return nil
}

func (s *Service) reconcileApprovalEffects(
	ctx context.Context,
	value approval.Approval,
) (bool, error) {
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
		state, ok := s.effects.Lookup(command)
		if !ok || state != idempotency.StateUnknown {
			continue
		}
		if _, err := s.effects.Reconcile(ctx, command); err != nil &&
			!errors.Is(err, idempotency.ErrRetryableFailure) &&
			!errors.Is(err, idempotency.ErrReconciliationPending) &&
			!errors.Is(err, idempotency.ErrManualReview) {
			return false, err
		}
	}
	results, _ := s.approvalEffectResults(value)
	if !executionRequiresReconciliation(results) {
		return false, nil
	}
	if _, err := s.approvals.MarkReconciliationRequired(ctx, value.ID, results); err != nil {
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
				s.recordFailure(value.RunID, err)
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
		state, ok := s.effects.Lookup(idempotency.Command{
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
	return status == domain.RunCompleted || status == domain.RunRejected || status == domain.RunFailed
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
		if _, err := s.journal.Append(ctx, run.RunID, audit.Draft{
			EventID: fmt.Sprintf("run:%s:attribution:%d", run.RunID, planVersion),
			Actor:   audit.ActorAgent,
			Type:    audit.EventAttribution,
			Payload: map[string]any{
				"summary": "司机疲劳驾驶与服务区长时间停留导致延误，天气因素已排除。",
				"evidence": []map[string]any{
					{"label": "连续驾驶", "value": "9 小时"},
					{"label": "异常停留", "value": "绵阳北服务区 6 小时"},
					{"label": "天气", "value": "晴，无预警"},
				},
				"plan_version": planVersion,
			},
		}); err != nil {
			return err
		}
		_, err := s.createApproval(ctx, run, planVersion, outcome)
		if err != nil {
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

func (s *Service) createApproval(
	ctx context.Context,
	run RunView,
	planVersion int,
	outcome agentkit.Outcome,
) (approval.Approval, error) {
	items := make([]approval.Item, 0, len(outcome.Interrupts))
	callIDs := make([]string, 0, len(outcome.Interrupts))
	runContext := domain.RunContext{
		RunID:       run.RunID,
		IncidentID:  run.IncidentID,
		WaybillID:   run.WaybillID,
		PlanVersion: planVersion,
	}
	for _, interrupt := range outcome.Interrupts {
		write, err := s.registry.ParseWrite(interrupt.WireName, interrupt.Arguments)
		if err != nil {
			return approval.Approval{}, err
		}
		if write.Action != interrupt.Action {
			return approval.Approval{}, fmt.Errorf(
				"interrupt action %q does not match tool action %q",
				interrupt.Action,
				write.Action,
			)
		}
		if write.LegacyKey != "" {
			return approval.Approval{}, fmt.Errorf(
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
			return approval.Approval{}, err
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
		callIDs = append(callIDs, interrupt.CallID)
	}
	value := approval.Approval{
		ID:          approval.IDFor(run.RunID, callIDs),
		RunID:       run.RunID,
		SDKRunID:    outcome.SDKRunID,
		WaybillID:   run.WaybillID,
		PlanVersion: planVersion,
		Items:       items,
		Reason:      "降低延误风险并通知货主新的承运安排。",
		Evidence: []approval.Evidence{
			{Label: "连续驾驶", Value: "9 小时"},
			{Label: "异常停留", Value: "绵阳北服务区 6 小时"},
			{Label: "天气", Value: "晴，无预警"},
		},
		ExpiresAt: s.clock().UTC().Add(s.ttl),
	}
	created, err := s.approvals.Create(ctx, value)
	if err != nil {
		return approval.Approval{}, err
	}
	s.scheduleExpiration(created)
	return created, nil
}

func (s *Service) rebuildRuns() error {
	projected, err := projectRuns(s.journal.AllEvents())
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

func (s *Service) recordFailure(runID domain.RunID, cause error) {
	event, err := s.journal.Append(context.Background(), runID, audit.Draft{
		EventID: "run:" + string(runID) + ":failed",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunFailed,
		Payload: map[string]any{"status": domain.RunFailed, "error": cause.Error()},
	})
	run := s.run(runID)
	run.Status = domain.RunFailed
	if err == nil {
		run.LastSeq = event.Seq
	}
	s.setRun(run)
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
