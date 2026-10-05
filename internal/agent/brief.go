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

type BriefFailureReason string

const (
	BriefFailureTimeout  BriefFailureReason = "timeout"
	BriefFailureProvider BriefFailureReason = "provider_error"
	BriefFailureSchema   BriefFailureReason = "schema"
	BriefFailureEvidence BriefFailureReason = "evidence"
)

type briefError struct {
	reason BriefFailureReason
	err    error
}

func (e *briefError) Error() string {
	return e.err.Error()
}

func (e *briefError) Unwrap() error {
	return e.err
}

func BriefFailureOf(err error) BriefFailureReason {
	if errors.Is(err, context.DeadlineExceeded) {
		return BriefFailureTimeout
	}
	var classified *briefError
	if errors.As(err, &classified) {
		return classified.reason
	}
	return BriefFailureProvider
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
	return newModelBriefGenerator(model, config.BriefTimeout), nil
}

func newModelBriefGenerator(model briefModel, timeout time.Duration) BriefGenerator {
	return &modelBriefGenerator{model: model, timeout: timeout}
}

func (g *modelBriefGenerator) Generate(
	ctx context.Context,
	input BriefInput,
) (GeneratedBrief, error) {
	if g == nil || g.model == nil {
		return GeneratedBrief{}, newBriefError(
			BriefFailureProvider,
			fmt.Errorf("brief model is required"),
		)
	}
	if err := ctx.Err(); err != nil {
		return GeneratedBrief{}, newBriefError(briefContextFailure(err), err)
	}
	timeout := g.timeout
	if timeout <= 0 {
		timeout = DefaultBriefTimeout
	}

	request, err := briefRequest(input)
	if err != nil {
		return GeneratedBrief{}, newBriefError(BriefFailureSchema, err)
	}
	var response *responses.Response
	for attempt := 1; attempt <= maxBriefAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		response, err = g.model.NewResponses(attemptCtx, request)
		attemptErr := attemptCtx.Err()
		cancel()
		if attemptErr != nil {
			return GeneratedBrief{}, newBriefError(
				briefContextFailure(attemptErr),
				attemptErr,
			)
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return GeneratedBrief{}, newBriefError(
				briefContextFailure(ctx.Err()),
				ctx.Err(),
			)
		}
	}
	if err != nil {
		return GeneratedBrief{}, newBriefError(
			BriefFailureProvider,
			fmt.Errorf("generate executive brief: %w", err),
		)
	}
	if err := validateHistoryValue("brief model response", response); err != nil {
		return GeneratedBrief{}, newBriefError(BriefFailureSchema, err)
	}
	text, err := briefResponseText(response)
	if err != nil {
		return GeneratedBrief{}, err
	}
	return parseGeneratedBrief(text)
}

func newBriefError(reason BriefFailureReason, err error) error {
	if err == nil {
		return nil
	}
	return &briefError{reason: reason, err: err}
}

func briefContextFailure(err error) BriefFailureReason {
	if errors.Is(err, context.DeadlineExceeded) {
		return BriefFailureTimeout
	}
	return BriefFailureProvider
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
		return "", newBriefError(
			BriefFailureProvider,
			fmt.Errorf("brief model returned no response"),
		)
	}
	if response.Error != nil {
		return "", newBriefError(
			BriefFailureProvider,
			fmt.Errorf("brief model error: %s", response.Error.Message),
		)
	}
	var result strings.Builder
	for _, output := range response.Output {
		if output.OfFunctionCall != nil ||
			output.OfImageGenerationCall != nil ||
			output.OfWebSearchCall != nil ||
			output.OfCodeInterpreterCall != nil {
			return "", newBriefError(
				BriefFailureSchema,
				fmt.Errorf("brief model returned a non-text action"),
			)
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
		return "", newBriefError(
			BriefFailureSchema,
			fmt.Errorf("brief model returned empty text"),
		)
	}
	return text, nil
}

func parseGeneratedBrief(raw string) (GeneratedBrief, error) {
	if len(raw) > maxBriefOutputBytes {
		return GeneratedBrief{}, newBriefError(
			BriefFailureSchema,
			fmt.Errorf("executive brief exceeds output limit"),
		)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope modelBriefEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return GeneratedBrief{}, newBriefError(
			BriefFailureSchema,
			fmt.Errorf("decode executive brief: %w", err),
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return GeneratedBrief{}, newBriefError(
			BriefFailureSchema,
			fmt.Errorf("decode executive brief: %w", err),
		)
	}
	if len(envelope.Items) != 3 {
		return GeneratedBrief{}, newBriefError(
			BriefFailureSchema,
			fmt.Errorf("executive brief requires exactly three items"),
		)
	}
	result := GeneratedBrief{Items: make([]GeneratedBriefItem, 0, 3)}
	for index, item := range envelope.Items {
		item.Headline = strings.TrimSpace(item.Headline)
		item.Body = strings.TrimSpace(item.Body)
		if item.Headline == "" || item.Body == "" {
			return GeneratedBrief{}, newBriefError(
				BriefFailureSchema,
				fmt.Errorf("executive brief item %d has empty prose", index),
			)
		}
		if utf8.RuneCountInString(item.Headline) > maxBriefHeadlineRunes ||
			utf8.RuneCountInString(item.Body) > maxBriefBodyRunes {
			return GeneratedBrief{}, newBriefError(
				BriefFailureSchema,
				fmt.Errorf("executive brief item %d exceeds text limits", index),
			)
		}
		if len(item.EvidenceIDs) == 0 || len(item.EvidenceIDs) > 3 {
			return GeneratedBrief{}, newBriefError(
				BriefFailureEvidence,
				fmt.Errorf(
					"executive brief item %d requires one to three evidence IDs",
					index,
				),
			)
		}
		seen := make(map[string]struct{}, len(item.EvidenceIDs))
		for _, evidenceID := range item.EvidenceIDs {
			if _, ok := briefEvidenceIDs[evidenceID]; !ok {
				return GeneratedBrief{}, newBriefError(
					BriefFailureEvidence,
					fmt.Errorf(
						"executive brief item %d cites unknown evidence %q",
						index,
						evidenceID,
					),
				)
			}
			if _, duplicate := seen[evidenceID]; duplicate {
				return GeneratedBrief{}, newBriefError(
					BriefFailureEvidence,
					fmt.Errorf(
						"executive brief item %d repeats evidence %q",
						index,
						evidenceID,
					),
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
		return GeneratedBrief{}, newBriefError(BriefFailureSchema, err)
	}
	return result, nil
}
