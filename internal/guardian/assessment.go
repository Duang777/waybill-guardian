package guardian

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

type waybillFacts struct {
	Waybill  platform.Waybill
	Tracking []platform.TrackPoint
	Driver   platform.Driver
	Weather  []platform.RoadWeather
}

type assessment struct {
	Summary  string
	Reason   string
	Evidence []approval.Evidence
	Risk     RiskScore
}

func (s *Service) loadWaybillFacts(
	ctx context.Context,
	waybillID domain.WaybillID,
) (waybillFacts, error) {
	waybill, err := s.reads.TMS.GetWaybill(ctx, platform.GetWaybillRequest{
		WaybillID: waybillID,
	})
	if err != nil {
		return waybillFacts{}, err
	}
	tracking, err := s.reads.TMS.GetTracking(ctx, platform.GetTrackingRequest{
		WaybillID: waybillID,
	})
	if err != nil {
		return waybillFacts{}, err
	}
	driver, err := s.reads.TMS.GetDriver(ctx, platform.GetDriverRequest{
		DriverID: waybill.DriverID,
	})
	if err != nil {
		return waybillFacts{}, err
	}
	weather, err := s.reads.Weather.GetRoadWeather(
		ctx,
		platform.GetRoadWeatherRequest{
			Route: waybill.Origin + "-" + waybill.Destination,
		},
	)
	if err != nil {
		return waybillFacts{}, err
	}
	return waybillFacts{
		Waybill:  waybill,
		Tracking: tracking,
		Driver:   driver,
		Weather:  weather,
	}, nil
}

func deriveAssessment(facts waybillFacts) assessment {
	evidence := []approval.Evidence{{
		Label: "连续驾驶",
		Value: formatHours(facts.Driver.ContinuousDriveHrs),
	}}
	factors := make([]string, 0, 3)
	if facts.Driver.FatigueAlert {
		factors = append(factors, "司机疲劳预警")
	}

	anomalies := make([]string, 0)
	totalStopHours := 0.0
	for _, point := range facts.Tracking {
		if !point.Anomaly {
			continue
		}
		value := point.Label
		if point.StopHours > 0 {
			value += "停留" + formatHours(point.StopHours)
			totalStopHours += point.StopHours
		}
		anomalies = append(anomalies, value)
	}
	if len(anomalies) > 0 {
		evidence = append(evidence, approval.Evidence{
			Label: "异常停留",
			Value: strings.Join(anomalies, "；"),
		})
		factors = append(factors, strings.Join(anomalies, "、"))
	}

	weatherEvidence := make([]string, 0, len(facts.Weather))
	weatherRisk := 0
	for _, item := range facts.Weather {
		weatherEvidence = append(weatherEvidence, fmt.Sprintf(
			"%s%s，预警级别%s",
			item.Segment,
			item.Condition,
			item.AlertLevel,
		))
		weatherRisk = max(weatherRisk, weatherAlertRisk(item.AlertLevel))
	}
	evidence = append(evidence, approval.Evidence{
		Label: "沿途天气",
		Value: strings.Join(weatherEvidence, "；"),
	})
	if weatherRisk > 0 {
		factors = append(factors, "沿途天气预警")
	}

	summary := "当前证据未发现明确异常因素。"
	if len(factors) > 0 {
		summary = strings.Join(factors, "、") + "共同构成当前运输风险。"
	}
	statusRisk := 0
	if !strings.EqualFold(facts.Waybill.Status, "delivered") {
		statusRisk = 20
	}
	etaRisk := clampScore(statusRisk + len(anomalies)*15 + int(math.Round(totalStopHours*8)))
	roadRisk := clampScore(
		int(math.Round(facts.Driver.ContinuousDriveHrs*5)) +
			boolScore(facts.Driver.FatigueAlert, 30),
	)
	return assessment{
		Summary: summary,
		Reason: fmt.Sprintf(
			"基于%s至%s运单的轨迹、司机和天气证据执行处置。",
			facts.Waybill.Origin,
			facts.Waybill.Destination,
		),
		Evidence: evidence,
		Risk: RiskScore{
			ETADelay: etaRisk,
			Road:     roadRisk,
			Weather:  weatherRisk,
		},
	}
}

func formatHours(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64) + " 小时"
}

func weatherAlertRisk(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "none", "normal", "green":
		return 0
	case "low", "blue", "yellow":
		return 30
	case "medium", "orange":
		return 60
	case "high", "red", "critical":
		return 90
	default:
		return 20
	}
}

func boolScore(value bool, score int) int {
	if value {
		return score
	}
	return 0
}

func clampScore(value int) int {
	return min(max(value, 0), 100)
}
