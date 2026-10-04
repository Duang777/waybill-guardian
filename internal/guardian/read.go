package guardian

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

type WaybillCatalogItem struct {
	WaybillID        domain.WaybillID `json:"waybill_id"`
	Origin           string           `json:"origin"`
	Destination      string           `json:"destination"`
	OriginHubID      platform.HubID   `json:"origin_hub_id,omitempty"`
	DestinationHubID platform.HubID   `json:"destination_hub_id,omitempty"`
	RouteID          platform.RouteID `json:"route_id,omitempty"`
	Status           string           `json:"status"`
	HasAnomaly       bool             `json:"has_anomaly"`
	AnomalyLabel     string           `json:"anomaly_label,omitempty"`
	AnomalyType      string           `json:"anomaly_type,omitempty"`
	LastRecordedAt   string           `json:"last_recorded_at"`
}

type CarrierDTO struct {
	ID             domain.CarrierID `json:"carrier_id"`
	Name           string           `json:"name"`
	ETAHours       int              `json:"eta_hours"`
	ReliabilityPct float64          `json:"reliability_pct"`
}

type WaybillDTO struct {
	ID                domain.WaybillID   `json:"waybill_id"`
	Origin            string             `json:"origin"`
	Destination       string             `json:"destination"`
	OriginHubID       platform.HubID     `json:"origin_hub_id,omitempty"`
	DestinationHubID  platform.HubID     `json:"destination_hub_id,omitempty"`
	RouteID           platform.RouteID   `json:"route_id,omitempty"`
	VehicleID         platform.VehicleID `json:"vehicle_id,omitempty"`
	Cargo             string             `json:"cargo"`
	CarrierID         domain.CarrierID   `json:"carrier_id"`
	DriverID          domain.DriverID    `json:"driver_id"`
	Status            string             `json:"status"`
	SLAHours          int                `json:"sla_hours"`
	ShipperPhone      string             `json:"shipper_phone"`
	CandidateCarriers []CarrierDTO       `json:"candidate_carriers"`
}

type TrackPointDTO struct {
	Label       string  `json:"label"`
	RecordedAt  string  `json:"recorded_at"`
	Longitude   float64 `json:"longitude"`
	Latitude    float64 `json:"latitude"`
	SpeedKPH    int     `json:"speed_kph"`
	StopHours   float64 `json:"stop_hours,omitempty"`
	Anomaly     bool    `json:"anomaly"`
	AnomalyType string  `json:"anomaly_type,omitempty"`
}

type DriverDTO struct {
	ID                 domain.DriverID `json:"driver_id"`
	Name               string          `json:"name"`
	Phone              string          `json:"phone"`
	Plate              string          `json:"plate"`
	ContinuousDriveHrs float64         `json:"continuous_drive_hours"`
	FatigueAlert       bool            `json:"fatigue_alert"`
}

type RoadWeatherDTO struct {
	Segment    string `json:"segment"`
	Condition  string `json:"condition"`
	AlertLevel string `json:"alert_level"`
}

type RiskScore struct {
	ETADelay int `json:"eta_delay"`
	Road     int `json:"road"`
	Weather  int `json:"weather"`
}

type WaybillView struct {
	Waybill  WaybillDTO       `json:"waybill"`
	Tracking []TrackPointDTO  `json:"tracking"`
	Driver   DriverDTO        `json:"driver"`
	Weather  []RoadWeatherDTO `json:"weather"`
	Risk     RiskScore        `json:"risk"`
}

type RunSummary struct {
	RunID      domain.RunID      `json:"run_id"`
	IncidentID domain.IncidentID `json:"incident_id"`
	WaybillID  domain.WaybillID  `json:"waybill_id"`
	Status     domain.RunStatus  `json:"status"`
	LastSeq    audit.Seq         `json:"last_seq"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type PendingApprovalSummary struct {
	ID          domain.ApprovalID `json:"id"`
	RunID       domain.RunID      `json:"run_id"`
	WaybillID   domain.WaybillID  `json:"waybill_id"`
	PlanVersion int               `json:"plan_version"`
	RequestedAt time.Time         `json:"requested_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

type RunSnapshot struct {
	Run    RunSummary    `json:"run"`
	Events []audit.Event `json:"events"`
}

func (s *Service) ListWaybills(
	ctx context.Context,
) ([]WaybillCatalogItem, error) {
	if err := s.beginOperation(); err != nil {
		return nil, err
	}
	defer s.wg.Done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := s.reads.Catalog.ListWaybills(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]WaybillCatalogItem, 0, len(values))
	for _, value := range values {
		result = append(result, WaybillCatalogItem{
			WaybillID:        value.WaybillID,
			Origin:           value.Origin,
			Destination:      value.Destination,
			OriginHubID:      value.OriginHubID,
			DestinationHubID: value.DestinationHubID,
			RouteID:          value.RouteID,
			Status:           value.Status,
			HasAnomaly:       value.HasAnomaly,
			AnomalyLabel:     value.AnomalyLabel,
			AnomalyType:      value.AnomalyType,
			LastRecordedAt:   value.LastRecordedAt.UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (s *Service) GetWaybill(
	ctx context.Context,
	id domain.WaybillID,
) (WaybillView, error) {
	if err := s.beginOperation(); err != nil {
		return WaybillView{}, err
	}
	defer s.wg.Done()
	if err := ctx.Err(); err != nil {
		return WaybillView{}, err
	}
	if err := domain.ValidateWaybillID(id); err != nil {
		return WaybillView{}, err
	}
	facts, err := s.loadWaybillFacts(ctx, id)
	if err != nil {
		return WaybillView{}, err
	}
	assessment := deriveAssessment(facts)
	return mapWaybillView(facts, assessment.Risk), nil
}

func mapWaybillView(facts waybillFacts, risk RiskScore) WaybillView {
	carriers := make([]CarrierDTO, 0, len(facts.Waybill.CandidateCarriers))
	for _, carrier := range facts.Waybill.CandidateCarriers {
		carriers = append(carriers, CarrierDTO{
			ID:             carrier.ID,
			Name:           carrier.Name,
			ETAHours:       carrier.ETAHours,
			ReliabilityPct: carrier.ReliabilityPct,
		})
	}
	tracking := make([]TrackPointDTO, 0, len(facts.Tracking))
	for _, point := range facts.Tracking {
		tracking = append(tracking, TrackPointDTO{
			Label:       point.Label,
			RecordedAt:  point.RecordedAt,
			Longitude:   point.Longitude,
			Latitude:    point.Latitude,
			SpeedKPH:    point.SpeedKPH,
			StopHours:   point.StopHours,
			Anomaly:     point.Anomaly,
			AnomalyType: point.AnomalyType,
		})
	}
	weather := make([]RoadWeatherDTO, 0, len(facts.Weather))
	for _, item := range facts.Weather {
		weather = append(weather, RoadWeatherDTO{
			Segment:    item.Segment,
			Condition:  item.Condition,
			AlertLevel: item.AlertLevel,
		})
	}
	return WaybillView{
		Waybill: WaybillDTO{
			ID:                facts.Waybill.ID,
			Origin:            facts.Waybill.Origin,
			Destination:       facts.Waybill.Destination,
			OriginHubID:       facts.Waybill.OriginHubID,
			DestinationHubID:  facts.Waybill.DestinationHubID,
			RouteID:           facts.Waybill.RouteID,
			VehicleID:         facts.Waybill.VehicleID,
			Cargo:             facts.Waybill.Cargo,
			CarrierID:         facts.Waybill.CarrierID,
			DriverID:          facts.Waybill.DriverID,
			Status:            facts.Waybill.Status,
			SLAHours:          facts.Waybill.SLAHours,
			ShipperPhone:      audit.MaskPhone(facts.Waybill.ShipperPhone),
			CandidateCarriers: carriers,
		},
		Tracking: tracking,
		Driver: DriverDTO{
			ID:                 facts.Driver.ID,
			Name:               facts.Driver.Name,
			Phone:              audit.MaskPhone(facts.Driver.Phone),
			Plate:              audit.MaskPlate(facts.Driver.Plate),
			ContinuousDriveHrs: facts.Driver.ContinuousDriveHrs,
			FatigueAlert:       facts.Driver.FatigueAlert,
		},
		Weather: weather,
		Risk:    risk,
	}
}

func (s *Service) ListActiveRuns(ctx context.Context) ([]RunSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.recovery != nil {
		projections, err := s.recovery.RunProjections(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]RunSummary, 0, len(projections))
		for _, run := range projections {
			if !isOperationallyActive(run.Status) {
				continue
			}
			result = append(result, RunSummary{
				RunID:      run.RunID,
				IncidentID: run.IncidentID,
				WaybillID:  run.WaybillID,
				Status:     run.Status,
				LastSeq:    run.LastSeq,
				UpdatedAt:  run.UpdatedAt,
			})
		}
		sortRunSummaries(result)
		return result, nil
	}
	events, err := s.journal.AllEvents(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := projectRuns(events)
	if err != nil {
		return nil, err
	}
	result := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		if isOperationallyActive(run.Status) {
			result = append(result, run)
		}
	}
	sortRunSummaries(result)
	return result, nil
}

func (s *Service) ListPendingApprovals(ctx context.Context) ([]PendingApprovalSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]PendingApprovalSummary, 0)
	for _, value := range s.approvals.List() {
		if value.Status != approval.StatusPending {
			continue
		}
		result = append(result, PendingApprovalSummary{
			ID:          value.ID,
			RunID:       value.RunID,
			WaybillID:   value.WaybillID,
			PlanVersion: value.PlanVersion,
			RequestedAt: value.RequestedAt,
			ExpiresAt:   value.ExpiresAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ExpiresAt.Equal(result[j].ExpiresAt) {
			if result[i].RequestedAt.Equal(result[j].RequestedAt) {
				return result[i].ID < result[j].ID
			}
			return result[i].RequestedAt.Before(result[j].RequestedAt)
		}
		return result[i].ExpiresAt.Before(result[j].ExpiresAt)
	})
	return result, nil
}

func (s *Service) Snapshot(ctx context.Context, runID domain.RunID) (RunSnapshot, error) {
	if s.recovery != nil {
		projection, err := s.recovery.RunProjection(ctx, runID)
		if err != nil {
			return RunSnapshot{}, err
		}
		run := RunSummary{
			RunID:      projection.RunID,
			IncidentID: projection.IncidentID,
			WaybillID:  projection.WaybillID,
			Status:     projection.Status,
			LastSeq:    projection.LastSeq,
			UpdatedAt:  projection.UpdatedAt,
		}
		if projection.Status == domain.RunManualReview {
			return RunSnapshot{Run: run, Events: []audit.Event{}}, nil
		}
		events, err := s.journal.Replay(ctx, runID, 0)
		if err != nil {
			return RunSnapshot{}, err
		}
		return RunSnapshot{Run: run, Events: events}, nil
	}
	events, err := s.journal.Replay(ctx, runID, 0)
	if err != nil {
		return RunSnapshot{}, err
	}
	run, err := projectRun(events)
	if err != nil {
		return RunSnapshot{}, err
	}
	return RunSnapshot{Run: run, Events: events}, nil
}

func sortRunSummaries(values []RunSummary) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].RunID < values[j].RunID
		}
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
}

func isOperationallyActive(status domain.RunStatus) bool {
	return !isTerminal(status) || status == domain.RunManualReview
}

func projectRuns(events []audit.Event) (map[domain.RunID]RunSummary, error) {
	grouped := make(map[domain.RunID][]audit.Event)
	for _, event := range events {
		grouped[event.RunID] = append(grouped[event.RunID], event)
	}
	result := make(map[domain.RunID]RunSummary, len(grouped))
	for runID, runEvents := range grouped {
		run, err := projectRun(runEvents)
		if err != nil {
			return nil, fmt.Errorf("project run %q: %w", runID, err)
		}
		result[runID] = run
	}
	return result, nil
}

func projectRun(events []audit.Event) (RunSummary, error) {
	if len(events) == 0 {
		return RunSummary{}, audit.ErrRunNotFound
	}
	var run RunSummary
	for index, event := range events {
		if index > 0 && event.Seq != events[index-1].Seq+1 {
			return RunSummary{}, fmt.Errorf("non-contiguous event sequence at %d", event.Seq)
		}
		run.RunID = event.RunID
		run.LastSeq = event.Seq
		run.UpdatedAt = event.TS
		switch event.Type {
		case audit.EventRunStarted:
			var payload runStartedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return RunSummary{}, err
			}
			run.IncidentID = payload.IncidentID
			run.WaybillID = payload.WaybillID
			run.Status = payload.Status
		case audit.EventApprovalRequested:
			run.Status = domain.RunAwaitingApproval
		case audit.EventApprovalDecided, audit.EventApprovalReconciliationRequired:
			run.Status = domain.RunExecuting
		case audit.EventApprovalExecutionFailed, audit.EventRunFailed:
			run.Status = domain.RunFailed
		case audit.EventRunReviewRequired:
			run.Status = domain.RunReviewRequired
		case audit.EventRunCompleted:
			run.Status = domain.RunCompleted
		case audit.EventRunRejected:
			run.Status = domain.RunRejected
		}
	}
	if run.RunID == "" || run.IncidentID == "" || run.WaybillID == "" || run.LastSeq == 0 {
		return RunSummary{}, fmt.Errorf("run event prefix is incomplete")
	}
	return run, nil
}
