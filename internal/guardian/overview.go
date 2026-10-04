package guardian

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

type Overview struct {
	AsOf                time.Time                 `json:"as_of"`
	DataMode            string                    `json:"data_mode"`
	NetworkAvailable    bool                      `json:"network_available"`
	Totals              OverviewTotals            `json:"totals"`
	Hubs                []HubOverview             `json:"hubs"`
	Routes              []RouteOverview           `json:"routes"`
	Anomalies           []AnomalyOverview         `json:"anomalies"`
	AnomalyDistribution []AnomalyDistributionItem `json:"anomaly_distribution"`
	Brief               ExecutiveBrief            `json:"brief"`
}

type OverviewTotals struct {
	Waybills  int `json:"waybills"`
	Anomalies int `json:"anomalies"`
	InFlight  int `json:"in_flight"`
	Handling  int `json:"handling"`
	Closed    int `json:"closed"`
}

type HubOverview struct {
	ID             platform.HubID   `json:"hub_id"`
	Name           string           `json:"name"`
	Province       string           `json:"province"`
	City           string           `json:"city"`
	Longitude      float64          `json:"longitude"`
	Latitude       float64          `json:"latitude"`
	DailyCapacity  int              `json:"daily_capacity"`
	Waybills       int              `json:"waybills"`
	InFlight       int              `json:"in_flight"`
	Anomalies      int              `json:"anomalies"`
	Handling       int              `json:"handling"`
	Closed         int              `json:"closed"`
	FocusWaybillID domain.WaybillID `json:"focus_waybill_id,omitempty"`
}

type RouteOverview struct {
	ID               platform.RouteID `json:"route_id"`
	OriginHubID      platform.HubID   `json:"origin_hub_id"`
	DestinationHubID platform.HubID   `json:"destination_hub_id"`
	DistanceKM       int              `json:"distance_km"`
	StandardHours    int              `json:"standard_hours"`
	Waybills         int              `json:"waybills"`
	Anomalies        int              `json:"anomalies"`
	DelayHeat        int              `json:"delay_heat"`
	MaxRisk          int              `json:"max_risk"`
}

type AnomalyOverview struct {
	WaybillID        domain.WaybillID `json:"waybill_id"`
	Origin           string           `json:"origin"`
	Destination      string           `json:"destination"`
	OriginHubID      platform.HubID   `json:"origin_hub_id,omitempty"`
	DestinationHubID platform.HubID   `json:"destination_hub_id,omitempty"`
	RouteID          platform.RouteID `json:"route_id,omitempty"`
	Type             string           `json:"type"`
	Label            string           `json:"label"`
	LastRecordedAt   time.Time        `json:"last_recorded_at"`
	Risk             RiskScore        `json:"risk"`
	RiskScore        int              `json:"risk_score"`
	RunID            domain.RunID     `json:"run_id,omitempty"`
	RunStatus        domain.RunStatus `json:"run_status,omitempty"`
}

type AnomalyDistributionItem struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type ExecutiveBrief struct {
	Mode  string               `json:"mode"`
	Items []ExecutiveBriefItem `json:"items"`
}

type ExecutiveBriefItem struct {
	ID       string             `json:"id"`
	Headline string             `json:"headline"`
	Body     string             `json:"body"`
	Evidence []EvidenceCitation `json:"evidence"`
}

type EvidenceCitation struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

type KPIReport struct {
	Window      string         `json:"window"`
	AsOf        time.Time      `json:"as_of"`
	Assumptions KPIAssumptions `json:"assumptions"`
	Metrics     []KPIMetric    `json:"metrics"`
}

type KPIAssumptions struct {
	EvidenceStepMinutes float64 `json:"evidence_step_minutes"`
}

type KPIMetric struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Value        *float64 `json:"value"`
	Unit         string   `json:"unit"`
	Availability string   `json:"availability"`
	Formula      string   `json:"formula"`
	Reason       string   `json:"reason,omitempty"`
}

func (s *Service) Overview(
	ctx context.Context,
	waybillIDs []domain.WaybillID,
) (Overview, error) {
	if err := s.beginOperation(); err != nil {
		return Overview{}, err
	}
	defer s.wg.Done()

	catalog, err := s.scopedCatalog(ctx, waybillIDs)
	if err != nil {
		return Overview{}, err
	}
	runs, err := s.latestRunsByWaybill(ctx, waybillIDs)
	if err != nil {
		return Overview{}, err
	}
	hubs, routes, err := s.readNetwork(ctx)
	if err != nil {
		return Overview{}, err
	}

	result := Overview{
		AsOf:             catalogAsOf(catalog, s.clock().UTC()),
		DataMode:         dataMode(s.readSource),
		NetworkAvailable: len(hubs) > 0,
		Hubs:             make([]HubOverview, 0, len(hubs)),
		Routes:           make([]RouteOverview, 0, len(routes)),
		Anomalies:        make([]AnomalyOverview, 0),
	}
	hubIndex := make(map[platform.HubID]int, len(hubs))
	for _, hub := range hubs {
		hubIndex[hub.ID] = len(result.Hubs)
		result.Hubs = append(result.Hubs, HubOverview{
			ID:            hub.ID,
			Name:          hub.Name,
			Province:      hub.Province,
			City:          hub.City,
			Longitude:     hub.Longitude,
			Latitude:      hub.Latitude,
			DailyCapacity: hub.DailyCapacity,
		})
	}
	routeIndex := make(map[platform.RouteID]int, len(routes))
	for _, route := range routes {
		routeIndex[route.ID] = len(result.Routes)
		result.Routes = append(result.Routes, RouteOverview{
			ID:               route.ID,
			OriginHubID:      route.OriginHubID,
			DestinationHubID: route.DestinationHubID,
			DistanceKM:       route.DistanceKM,
			StandardHours:    route.StandardHours,
		})
	}

	distribution := make(map[string]int)
	for _, item := range catalog {
		result.Totals.Waybills++
		status := runs[item.WaybillID]
		inFlight := strings.EqualFold(item.Status, "in_transit")
		handling := status.RunID != "" && isOperationallyActive(status.Status)
		closed := status.Status == domain.RunCompleted || status.Status == domain.RunRejected
		if inFlight {
			result.Totals.InFlight++
		}
		if handling {
			result.Totals.Handling++
		}
		if closed {
			result.Totals.Closed++
		}
		if index, ok := hubIndex[item.DestinationHubID]; ok {
			hub := &result.Hubs[index]
			hub.Waybills++
			if inFlight {
				hub.InFlight++
			}
			if item.HasAnomaly {
				hub.Anomalies++
			}
			if handling {
				hub.Handling++
			}
			if closed {
				hub.Closed++
			}
		}
		if index, ok := routeIndex[item.RouteID]; ok {
			route := &result.Routes[index]
			route.Waybills++
			if item.HasAnomaly {
				route.Anomalies++
			}
		}
		if !item.HasAnomaly {
			continue
		}
		result.Totals.Anomalies++
		facts, loadErr := s.loadWaybillFacts(ctx, item.WaybillID)
		if loadErr != nil {
			return Overview{}, fmt.Errorf("load anomaly %q: %w", item.WaybillID, loadErr)
		}
		risk := deriveAssessment(facts).Risk
		overall := compositeRisk(risk)
		anomalyType := strings.TrimSpace(item.AnomalyType)
		if anomalyType == "" {
			anomalyType = "unknown"
		}
		distribution[anomalyType]++
		anomaly := AnomalyOverview{
			WaybillID:        item.WaybillID,
			Origin:           item.Origin,
			Destination:      item.Destination,
			OriginHubID:      item.OriginHubID,
			DestinationHubID: item.DestinationHubID,
			RouteID:          item.RouteID,
			Type:             anomalyType,
			Label:            item.AnomalyLabel,
			LastRecordedAt:   item.LastRecordedAt,
			Risk:             risk,
			RiskScore:        overall,
			RunID:            status.RunID,
			RunStatus:        status.Status,
		}
		result.Anomalies = append(result.Anomalies, anomaly)
		if index, ok := routeIndex[item.RouteID]; ok {
			result.Routes[index].MaxRisk = max(result.Routes[index].MaxRisk, overall)
		}
	}

	sort.Slice(result.Anomalies, func(i, j int) bool {
		if result.Anomalies[i].RiskScore == result.Anomalies[j].RiskScore {
			if result.Anomalies[i].LastRecordedAt.Equal(result.Anomalies[j].LastRecordedAt) {
				return result.Anomalies[i].WaybillID < result.Anomalies[j].WaybillID
			}
			return result.Anomalies[i].LastRecordedAt.After(result.Anomalies[j].LastRecordedAt)
		}
		return result.Anomalies[i].RiskScore > result.Anomalies[j].RiskScore
	})
	for _, anomaly := range result.Anomalies {
		for _, hubID := range []platform.HubID{anomaly.OriginHubID, anomaly.DestinationHubID} {
			if index, ok := hubIndex[hubID]; ok && result.Hubs[index].FocusWaybillID == "" {
				result.Hubs[index].FocusWaybillID = anomaly.WaybillID
			}
		}
	}
	for index := range result.Routes {
		route := &result.Routes[index]
		if route.Waybills > 0 {
			route.DelayHeat = percentage(route.Anomalies, route.Waybills)
		}
	}
	for anomalyType, count := range distribution {
		result.AnomalyDistribution = append(
			result.AnomalyDistribution,
			AnomalyDistributionItem{Type: anomalyType, Count: count},
		)
	}
	sort.Slice(result.AnomalyDistribution, func(i, j int) bool {
		if result.AnomalyDistribution[i].Count == result.AnomalyDistribution[j].Count {
			return result.AnomalyDistribution[i].Type < result.AnomalyDistribution[j].Type
		}
		return result.AnomalyDistribution[i].Count > result.AnomalyDistribution[j].Count
	})
	result.Brief = buildExecutiveBrief(result)
	return result, nil
}

func (s *Service) KPIs(
	ctx context.Context,
	waybillIDs []domain.WaybillID,
	window time.Duration,
) (KPIReport, error) {
	if err := s.beginOperation(); err != nil {
		return KPIReport{}, err
	}
	defer s.wg.Done()
	if window <= 0 {
		return KPIReport{}, fmt.Errorf("KPI window must be positive")
	}
	catalog, err := s.scopedCatalog(ctx, waybillIDs)
	if err != nil {
		return KPIReport{}, err
	}
	events, err := s.journal.AllEvents(ctx)
	if err != nil {
		return KPIReport{}, err
	}
	runs, err := projectRuns(events)
	if err != nil {
		return KPIReport{}, err
	}
	allowed := idSet(waybillIDs)
	asOf := anomalyCatalogAsOf(catalog, s.clock().UTC())
	for _, event := range events {
		run, ok := runs[event.RunID]
		if ok && allowedContains(allowed, run.WaybillID) && event.TS.After(asOf) {
			asOf = event.TS
		}
	}
	windowStart := asOf.Add(-window)

	anomalies := 0
	observed := 0
	closed := 0
	latest := latestRunMap(runs, allowed)
	for _, item := range catalog {
		if item.LastRecordedAt.Before(windowStart) || item.LastRecordedAt.After(asOf) {
			continue
		}
		observed++
		if !item.HasAnomaly {
			continue
		}
		anomalies++
		status := latest[item.WaybillID].Status
		if status == domain.RunCompleted || status == domain.RunRejected {
			closed++
		}
	}

	evidenceSteps := 0
	approved := 0
	decided := 0
	runStartedAt := make(map[domain.RunID]time.Time)
	completedDurations := make([]time.Duration, 0)
	for _, event := range events {
		run, ok := runs[event.RunID]
		if !ok || !allowedContains(allowed, run.WaybillID) {
			continue
		}
		if event.Type == audit.EventRunStarted {
			runStartedAt[event.RunID] = event.TS
		}
		if event.TS.Before(windowStart) || event.TS.After(asOf) {
			continue
		}
		switch event.Type {
		case audit.EventToolResult:
			var payload struct {
				Action domain.Action `json:"action"`
				Error  string        `json:"error"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil &&
				!payload.Action.IsWrite() &&
				payload.Error == "" {
				evidenceSteps++
			}
		case audit.EventApprovalDecided:
			if event.Actor != audit.ActorHuman {
				continue
			}
			var payload struct {
				Status approval.Status `json:"status"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil {
				continue
			}
			if payload.Status == approval.StatusConfirmed ||
				payload.Status == approval.StatusRejected {
				decided++
				if payload.Status == approval.StatusConfirmed {
					approved++
				}
			}
		case audit.EventRunCompleted, audit.EventRunRejected:
			if startedAt, ok := runStartedAt[event.RunID]; ok && !event.TS.Before(startedAt) {
				completedDurations = append(completedDurations, event.TS.Sub(startedAt))
			}
		}
	}

	report := KPIReport{
		Window: window.String(),
		AsOf:   asOf,
		Assumptions: KPIAssumptions{
			EvidenceStepMinutes: s.evidenceStepMinutes,
		},
		Metrics: []KPIMetric{
			unavailableMetric(
				"time_recovered_hours",
				"时效挽回",
				"小时",
				"sum(不处置预测 ETA - 处置后 ETA)",
				"数据源未提供不处置预测 ETA 与处置后 ETA",
			),
			unavailableMetric(
				"cost_impact_cny",
				"成本影响",
				"元",
				"sum(避免违约金 - 改派差价 - 处置成本)",
				"数据源未提供改派价格、违约金与处置成本",
			),
			availableMetric(
				"labor_saved_hours",
				"人力节省",
				float64(evidenceSteps)*s.evidenceStepMinutes/60,
				"小时",
				"成功自动证据采集步数 × 人工单步均时 ÷ 60",
			),
			availableMetric(
				"anomaly_closure_rate_pct",
				"异常闭环率",
				percentValue(closed, anomalies),
				"%",
				"窗口内已完成或已驳回处置的异常运单数 ÷ 窗口内异常运单数 × 100%",
			),
			availableMetric(
				"anomaly_rate_pct",
				"异常率",
				percentValue(anomalies, observed),
				"%",
				"窗口内异常运单数 ÷ 授权范围运单总数 × 100%",
			),
			durationMetric(completedDurations),
			ratioMetric(approved, decided),
		},
	}
	return report, nil
}

func (s *Service) scopedCatalog(
	ctx context.Context,
	waybillIDs []domain.WaybillID,
) ([]platform.WaybillSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allowed := idSet(waybillIDs)
	values, err := s.reads.Catalog.ListWaybills(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]platform.WaybillSummary, 0, len(waybillIDs))
	for _, value := range values {
		if allowedContains(allowed, value.WaybillID) {
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Service) latestRunsByWaybill(
	ctx context.Context,
	waybillIDs []domain.WaybillID,
) (map[domain.WaybillID]RunSummary, error) {
	var runs map[domain.RunID]RunSummary
	if s.recovery != nil {
		projections, err := s.recovery.RunProjections(ctx)
		if err != nil {
			return nil, err
		}
		runs = make(map[domain.RunID]RunSummary, len(projections))
		for _, projection := range projections {
			runs[projection.RunID] = RunSummary{
				RunID:      projection.RunID,
				IncidentID: projection.IncidentID,
				WaybillID:  projection.WaybillID,
				Status:     projection.Status,
				LastSeq:    projection.LastSeq,
				UpdatedAt:  projection.UpdatedAt,
			}
		}
	} else {
		events, err := s.journal.AllEvents(ctx)
		if err != nil {
			return nil, err
		}
		runs, err = projectRuns(events)
		if err != nil {
			return nil, err
		}
	}
	return latestRunMap(runs, idSet(waybillIDs)), nil
}

func (s *Service) readNetwork(
	ctx context.Context,
) ([]platform.Hub, []platform.Route, error) {
	if s.reads.Network == nil {
		return []platform.Hub{}, []platform.Route{}, nil
	}
	hubs, err := s.reads.Network.ListHubs(ctx)
	if err != nil {
		return nil, nil, err
	}
	routes, err := s.reads.Network.ListRoutes(ctx)
	if err != nil {
		return nil, nil, err
	}
	return hubs, routes, nil
}

func latestRunMap(
	runs map[domain.RunID]RunSummary,
	allowed map[domain.WaybillID]struct{},
) map[domain.WaybillID]RunSummary {
	result := make(map[domain.WaybillID]RunSummary)
	for _, run := range runs {
		if !allowedContains(allowed, run.WaybillID) {
			continue
		}
		current, ok := result[run.WaybillID]
		if !ok || run.UpdatedAt.After(current.UpdatedAt) ||
			(run.UpdatedAt.Equal(current.UpdatedAt) && run.RunID > current.RunID) {
			result[run.WaybillID] = run
		}
	}
	return result
}

func idSet(ids []domain.WaybillID) map[domain.WaybillID]struct{} {
	result := make(map[domain.WaybillID]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}

func allowedContains(allowed map[domain.WaybillID]struct{}, id domain.WaybillID) bool {
	_, ok := allowed[id]
	return ok
}

func catalogAsOf(values []platform.WaybillSummary, fallback time.Time) time.Time {
	result := time.Time{}
	for _, value := range values {
		if value.LastRecordedAt.After(result) {
			result = value.LastRecordedAt
		}
	}
	if result.IsZero() {
		return fallback
	}
	return result.UTC()
}

func anomalyCatalogAsOf(values []platform.WaybillSummary, fallback time.Time) time.Time {
	result := time.Time{}
	for _, value := range values {
		if value.HasAnomaly && value.LastRecordedAt.After(result) {
			result = value.LastRecordedAt
		}
	}
	if result.IsZero() {
		return catalogAsOf(values, fallback)
	}
	return result.UTC()
}

func compositeRisk(risk RiskScore) int {
	return (risk.ETADelay*5 + risk.Road*3 + risk.Weather*2 + 5) / 10
}

func percentage(numerator, denominator int) int {
	if denominator == 0 {
		return 0
	}
	return (numerator*100 + denominator/2) / denominator
}

func percentValue(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) * 100 / float64(denominator)
}

func dataMode(source string) string {
	switch {
	case strings.Contains(source, "simulated"):
		return "simulated"
	case strings.Contains(source, "fixture"):
		return "fixture"
	default:
		return "external"
	}
}

func buildExecutiveBrief(overview Overview) ExecutiveBrief {
	items := make([]ExecutiveBriefItem, 0, 3)
	if len(overview.AnomalyDistribution) > 0 {
		top := overview.AnomalyDistribution[0]
		items = append(items, ExecutiveBriefItem{
			ID:       "anomaly-mix",
			Headline: "优先治理高频异常类型",
			Body: fmt.Sprintf(
				"%s为当前首要异常类型，建议复盘其线路与承运环节的共性。",
				anomalyTypeLabel(top.Type),
			),
			Evidence: []EvidenceCitation{{
				Label:  anomalyTypeLabel(top.Type),
				Value:  fmt.Sprintf("%d 单，占异常 %d%%", top.Count, percentage(top.Count, overview.Totals.Anomalies)),
				Source: "anomaly_distribution",
			}},
		})
	} else {
		items = append(items, ExecutiveBriefItem{
			ID:       "anomaly-mix",
			Headline: "授权范围内暂无异常",
			Body:     "当前数据快照没有需要升级处置的异常运单。",
			Evidence: []EvidenceCitation{{
				Label:  "异常运单",
				Value:  "0 单",
				Source: "totals.anomalies",
			}},
		})
	}

	if route, ok := hottestRoute(overview.Routes); ok {
		items = append(items, ExecutiveBriefItem{
			ID:       "route-hotspot",
			Headline: "聚焦异常线路",
			Body:     "建议把线路级承运能力与天气预案优先投向当前热度最高的线路。",
			Evidence: []EvidenceCitation{{
				Label:  string(route.ID),
				Value:  fmt.Sprintf("%d 单异常，线路热度 %d%%", route.Anomalies, route.DelayHeat),
				Source: "routes",
			}},
		})
	} else {
		items = append(items, ExecutiveBriefItem{
			ID:       "route-hotspot",
			Headline: "补齐线路经营数据",
			Body:     "当前授权范围没有可计算的异常线路，暂不提出线路调整建议。",
			Evidence: []EvidenceCitation{{
				Label:  "可计算异常线路",
				Value:  "0 条",
				Source: "routes",
			}},
		})
	}

	if hub, ok := busiestAnomalyHub(overview.Hubs); ok {
		items = append(items, ExecutiveBriefItem{
			ID:       "hub-capacity",
			Headline: "前置公路港处置资源",
			Body:     fmt.Sprintf("建议在%s增加异常核验与接驳准备，缩短批量处置等待。", hub.Name),
			Evidence: []EvidenceCitation{
				{
					Label:  "异常运单",
					Value:  fmt.Sprintf("%d 单", hub.Anomalies),
					Source: "hubs",
				},
				{
					Label:  "处置中",
					Value:  fmt.Sprintf("%d 单", hub.Handling),
					Source: "hubs",
				},
			},
		})
	} else {
		items = append(items, ExecutiveBriefItem{
			ID:       "hub-capacity",
			Headline: "等待公路港网络数据",
			Body:     "当前数据源未提供可关联的公路港异常，暂不调整节点资源。",
			Evidence: []EvidenceCitation{{
				Label:  "网络数据",
				Value:  "不可用",
				Source: "network_available",
			}},
		})
	}
	return ExecutiveBrief{Mode: "deterministic_read_only", Items: items}
}

func hottestRoute(routes []RouteOverview) (RouteOverview, bool) {
	var result RouteOverview
	found := false
	for _, route := range routes {
		if route.Anomalies == 0 {
			continue
		}
		if !found || route.DelayHeat > result.DelayHeat ||
			(route.DelayHeat == result.DelayHeat && route.MaxRisk > result.MaxRisk) {
			result = route
			found = true
		}
	}
	return result, found
}

func busiestAnomalyHub(hubs []HubOverview) (HubOverview, bool) {
	var result HubOverview
	found := false
	for _, hub := range hubs {
		if hub.Anomalies == 0 {
			continue
		}
		if !found || hub.Anomalies > result.Anomalies ||
			(hub.Anomalies == result.Anomalies && hub.ID < result.ID) {
			result = hub
			found = true
		}
	}
	return result, found
}

func anomalyTypeLabel(value string) string {
	switch value {
	case "delay":
		return "时效延误"
	case "damage":
		return "货损"
	case "fatigue":
		return "疲劳驾驶"
	case "loss":
		return "货物丢失"
	case "weather":
		return "天气影响"
	default:
		return value
	}
}

func availableMetric(key, label string, value float64, unit, formula string) KPIMetric {
	return KPIMetric{
		Key:          key,
		Label:        label,
		Value:        &value,
		Unit:         unit,
		Availability: "available",
		Formula:      formula,
	}
}

func unavailableMetric(key, label, unit, formula, reason string) KPIMetric {
	return KPIMetric{
		Key:          key,
		Label:        label,
		Value:        nil,
		Unit:         unit,
		Availability: "unavailable",
		Formula:      formula,
		Reason:       reason,
	}
}

func durationMetric(values []time.Duration) KPIMetric {
	if len(values) == 0 {
		return unavailableMetric(
			"average_handling_minutes",
			"平均处置时长",
			"分钟",
			"sum(终态时间 - 启动时间) ÷ 窗口内闭环 run 数",
			"窗口内尚无闭环 run",
		)
	}
	total := time.Duration(0)
	for _, value := range values {
		total += value
	}
	return availableMetric(
		"average_handling_minutes",
		"平均处置时长",
		total.Minutes()/float64(len(values)),
		"分钟",
		"sum(终态时间 - 启动时间) ÷ 窗口内闭环 run 数",
	)
}

func ratioMetric(numerator, denominator int) KPIMetric {
	if denominator == 0 {
		return unavailableMetric(
			"approval_rate_pct",
			"人工审批通过率",
			"%",
			"人工确认数 ÷ 人工决定数 × 100%",
			"窗口内尚无人工审批决定",
		)
	}
	return availableMetric(
		"approval_rate_pct",
		"人工审批通过率",
		percentValue(numerator, denominator),
		"%",
		"人工确认数 ÷ 人工决定数 × 100%",
	)
}
