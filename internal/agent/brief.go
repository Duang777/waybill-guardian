package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
	"github.com/hastekit/agent-sdk-go/pkg/utils"
)

const (
	EvidenceFleetScope   = "fleet_scope"
	EvidenceAnomalyMix   = "anomaly_mix"
	EvidenceRouteHotspot = "route_hotspot"
	EvidenceHubPressure  = "hub_pressure"

	maxBriefAttempts      = 3
	defaultBriefTimeout   = 1500 * time.Millisecond
	maxBriefOutputBytes   = 8 << 10
	maxBriefHeadlineRunes = 60
	maxBriefBodyRunes     = 240
)

var briefEvidenceIDs = map[string]struct{}{
	EvidenceFleetScope:   {},
	EvidenceAnomalyMix:   {},
	EvidenceRouteHotspot: {},
	EvidenceHubPressure:  {},
}

type BriefInput struct {
	TotalWaybills         int    `json:"total_waybills"`
	TotalAnomalies        int    `json:"total_anomalies"`
	TopAnomalyType        string `json:"top_anomaly_type"`
	TopAnomalyCount       int    `json:"top_anomaly_count"`
	TopAnomalySharePct    int    `json:"top_anomaly_share_pct"`
	HottestRouteAnomalies int    `json:"hottest_route_anomalies"`
	HottestRouteHeatPct   int    `json:"hottest_route_heat_pct"`
	HottestRouteMaxRisk   int    `json:"hottest_route_max_risk"`
	BusiestHubAnomalies   int    `json:"busiest_hub_anomalies"`
	BusiestHubHandling    int    `json:"busiest_hub_handling"`
}

type GeneratedBrief struct {
	Items []GeneratedBriefItem
}

type GeneratedBriefItem struct {
	Headline    string
	Body        string
	EvidenceIDs []string
}

type BriefGenerator interface {
	Generate(context.Context, BriefInput) (GeneratedBrief, error)
}

type briefModel interface {
	NewResponses(context.Context, *responses.Request) (*responses.Response, error)
}

type modelBriefGenerator struct {
	model   briefModel
	timeout time.Duration
}

type modelBriefEnvelope struct {
	Items []modelBriefItem `json:"items"`
}

type modelBriefItem struct {
	Headline    string   `json:"headline"`
	Body        string   `json:"body"`
	EvidenceIDs []string `json:"evidence_ids"`
}

func NewBriefGenerator(config ModelConfig) (BriefGenerator, error) {
	mode, err := normalizeModelMode(config.Mode)
	if err != nil {
		return nil, err
	}
	if mode == ModeOffline {
		return nil, nil
	}
	model, err := newOnlineModel(config)
	if err != nil {
		return nil, err
	}
	return newModelBriefGenerator(model, defaultBriefTimeout), nil
}

func newModelBriefGenerator(model briefModel, timeout time.Duration) BriefGenerator {
	return &modelBriefGenerator{model: model, timeout: timeout}
}

func (g *modelBriefGenerator) Generate(
	ctx context.Context,
	input BriefInput,
) (GeneratedBrief, error) {
	if g == nil || g.model == nil {
		return GeneratedBrief{}, fmt.Errorf("brief model is required")
	}
	if err := ctx.Err(); err != nil {
		return GeneratedBrief{}, err
	}
	timeout := g.timeout
	if timeout <= 0 {
		timeout = defaultBriefTimeout
	}
	generationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := briefRequest(input)
	if err != nil {
		return GeneratedBrief{}, err
	}
	var response *responses.Response
	for attempt := 1; attempt <= maxBriefAttempts; attempt++ {
		response, err = g.model.NewResponses(generationCtx, request)
		if err == nil {
			break
		}
		if generationCtx.Err() != nil {
			return GeneratedBrief{}, generationCtx.Err()
		}
	}
	if err != nil {
		return GeneratedBrief{}, fmt.Errorf("generate executive brief: %w", err)
	}
	if err := validateHistoryValue("brief model response", response); err != nil {
		return GeneratedBrief{}, err
	}
	text, err := briefResponseText(response)
	if err != nil {
		return GeneratedBrief{}, err
	}
	return parseGeneratedBrief(text)
}

func briefRequest(input BriefInput) (*responses.Request, error) {
	facts, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode brief facts: %w", err)
	}
	instructions := `你是物流经营分析助手。只能依据输入的聚合事实生成三条中文建议。
返回严格 JSON：{"items":[{"headline":"...","body":"...","evidence_ids":["..."]}]}。
items 必须正好三项；每项至少引用一个允许的 evidence_id。不得输出运单、线路、港口、人员或车辆身份，不得调用工具。`
	prompt := fmt.Sprintf(
		"聚合事实：%s\n允许的 evidence_id：%s",
		facts,
		strings.Join([]string{
			EvidenceFleetScope,
			EvidenceAnomalyMix,
			EvidenceRouteHotspot,
			EvidenceHubPressure,
		}, ","),
	)
	store := false
	maxOutputTokens := 1000
	request := &responses.Request{
		Parameters: responses.Parameters{
			MaxOutputTokens: &maxOutputTokens,
			Store:           &store,
		},
		Input:        responses.InputUnion{OfString: utils.Ptr(prompt)},
		Instructions: &instructions,
		Tools:        []responses.ToolUnion{},
	}
	if err := validateHistoryValue("brief model request", request); err != nil {
		return nil, err
	}
	return request, nil
}

func briefResponseText(response *responses.Response) (string, error) {
	if response == nil {
		return "", fmt.Errorf("brief model returned no response")
	}
	if response.Error != nil {
		return "", fmt.Errorf("brief model error: %s", response.Error.Message)
	}
	var result strings.Builder
	for _, output := range response.Output {
		if output.OfFunctionCall != nil ||
			output.OfImageGenerationCall != nil ||
			output.OfWebSearchCall != nil ||
			output.OfCodeInterpreterCall != nil {
			return "", fmt.Errorf("brief model returned a non-text action")
		}
		if output.OfOutputMessage == nil || output.OfOutputMessage.Content == nil {
			continue
		}
		for _, content := range *output.OfOutputMessage.Content {
			if content.OfOutputText != nil {
				result.WriteString(content.OfOutputText.Text)
			}
		}
	}
	text := strings.TrimSpace(result.String())
	if text == "" {
		return "", fmt.Errorf("brief model returned empty text")
	}
	return text, nil
}

func parseGeneratedBrief(raw string) (GeneratedBrief, error) {
	if len(raw) > maxBriefOutputBytes {
		return GeneratedBrief{}, fmt.Errorf("executive brief exceeds output limit")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope modelBriefEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return GeneratedBrief{}, fmt.Errorf("decode executive brief: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return GeneratedBrief{}, fmt.Errorf("decode executive brief: %w", err)
	}
	if len(envelope.Items) != 3 {
		return GeneratedBrief{}, fmt.Errorf("executive brief requires exactly three items")
	}
	result := GeneratedBrief{Items: make([]GeneratedBriefItem, 0, 3)}
	for index, item := range envelope.Items {
		item.Headline = strings.TrimSpace(item.Headline)
		item.Body = strings.TrimSpace(item.Body)
		if item.Headline == "" || item.Body == "" {
			return GeneratedBrief{}, fmt.Errorf("executive brief item %d has empty prose", index)
		}
		if utf8.RuneCountInString(item.Headline) > maxBriefHeadlineRunes ||
			utf8.RuneCountInString(item.Body) > maxBriefBodyRunes {
			return GeneratedBrief{}, fmt.Errorf("executive brief item %d exceeds text limits", index)
		}
		if len(item.EvidenceIDs) == 0 || len(item.EvidenceIDs) > 3 {
			return GeneratedBrief{}, fmt.Errorf(
				"executive brief item %d requires one to three evidence IDs",
				index,
			)
		}
		seen := make(map[string]struct{}, len(item.EvidenceIDs))
		for _, evidenceID := range item.EvidenceIDs {
			if _, ok := briefEvidenceIDs[evidenceID]; !ok {
				return GeneratedBrief{}, fmt.Errorf(
					"executive brief item %d cites unknown evidence %q",
					index,
					evidenceID,
				)
			}
			if _, duplicate := seen[evidenceID]; duplicate {
				return GeneratedBrief{}, fmt.Errorf(
					"executive brief item %d repeats evidence %q",
					index,
					evidenceID,
				)
			}
			seen[evidenceID] = struct{}{}
		}
		result.Items = append(result.Items, GeneratedBriefItem{
			Headline:    item.Headline,
			Body:        item.Body,
			EvidenceIDs: append([]string(nil), item.EvidenceIDs...),
		})
	}
	if err := validateHistoryValue("generated brief", result); err != nil {
		return GeneratedBrief{}, err
	}
	return result, nil
}
