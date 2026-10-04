package guardian

import (
	"fmt"
	"strings"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
)

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

func briefInputForOverview(overview Overview) agentkit.BriefInput {
	input := agentkit.BriefInput{
		TotalWaybills:  overview.Totals.Waybills,
		TotalAnomalies: overview.Totals.Anomalies,
	}
	if len(overview.AnomalyDistribution) > 0 {
		top := overview.AnomalyDistribution[0]
		input.TopAnomalyType = top.Type
		input.TopAnomalyCount = top.Count
		input.TopAnomalySharePct = percentage(top.Count, overview.Totals.Anomalies)
	}
	if route, ok := hottestRoute(overview.Routes); ok {
		input.HottestRouteAnomalies = route.Anomalies
		input.HottestRouteHeatPct = route.DelayHeat
		input.HottestRouteMaxRisk = route.MaxRisk
	}
	if hub, ok := busiestAnomalyHub(overview.Hubs); ok {
		input.BusiestHubAnomalies = hub.Anomalies
		input.BusiestHubHandling = hub.Handling
	}
	return input
}

func materializeGeneratedBrief(
	input agentkit.BriefInput,
	generated agentkit.GeneratedBrief,
) (ExecutiveBrief, error) {
	if len(generated.Items) != 3 {
		return ExecutiveBrief{}, fmt.Errorf("generated brief requires exactly three items")
	}
	anomalyLabel := anomalyTypeLabel(input.TopAnomalyType)
	if anomalyLabel == "" {
		anomalyLabel = "异常运单"
	}
	ledger := map[string]EvidenceCitation{
		agentkit.EvidenceFleetScope: {
			Label:  "授权运单",
			Value:  fmt.Sprintf("%d 单", input.TotalWaybills),
			Source: "totals.waybills",
		},
		agentkit.EvidenceAnomalyMix: {
			Label: anomalyLabel,
			Value: fmt.Sprintf(
				"%d 单，占异常 %d%%",
				input.TopAnomalyCount,
				input.TopAnomalySharePct,
			),
			Source: "anomaly_distribution",
		},
		agentkit.EvidenceRouteHotspot: {
			Label: "最高热度线路",
			Value: fmt.Sprintf(
				"%d 单异常，热度 %d%%，最高风险 %d",
				input.HottestRouteAnomalies,
				input.HottestRouteHeatPct,
				input.HottestRouteMaxRisk,
			),
			Source: "routes",
		},
		agentkit.EvidenceHubPressure: {
			Label: "异常最集中公路港",
			Value: fmt.Sprintf(
				"%d 单异常，%d 单处置中",
				input.BusiestHubAnomalies,
				input.BusiestHubHandling,
			),
			Source: "hubs",
		},
	}
	result := ExecutiveBrief{
		Mode:  "model_read_only",
		Items: make([]ExecutiveBriefItem, 0, len(generated.Items)),
	}
	for index, item := range generated.Items {
		if strings.TrimSpace(item.Headline) == "" ||
			strings.TrimSpace(item.Body) == "" ||
			len(item.EvidenceIDs) == 0 {
			return ExecutiveBrief{}, fmt.Errorf("generated brief item %d is incomplete", index)
		}
		evidence := make([]EvidenceCitation, 0, len(item.EvidenceIDs))
		for _, evidenceID := range item.EvidenceIDs {
			citation, ok := ledger[evidenceID]
			if !ok {
				return ExecutiveBrief{}, fmt.Errorf(
					"generated brief item %d cites unknown evidence",
					index,
				)
			}
			evidence = append(evidence, citation)
		}
		result.Items = append(result.Items, ExecutiveBriefItem{
			ID:       fmt.Sprintf("model-brief-%d", index+1),
			Headline: item.Headline,
			Body:     item.Body,
			Evidence: evidence,
		})
	}
	return result, nil
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
