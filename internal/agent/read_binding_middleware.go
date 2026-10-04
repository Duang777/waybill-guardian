package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
)

type ReadBindingMiddleware struct {
	agents.NoopMiddleware
	reads platform.ReadSet
}

func NewReadBindingMiddleware(reads platform.ReadSet) *ReadBindingMiddleware {
	return &ReadBindingMiddleware{reads: reads}
}

func (m *ReadBindingMiddleware) WrapToolCall(
	next agents.ToolCallFunc,
) agents.ToolCallFunc {
	return func(
		ctx context.Context,
		tool *agents.BaseTool,
		call *agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		if isWrite(tool) {
			return next(ctx, tool, call)
		}
		runContext, err := parseRunContext(call.RunContext)
		if err != nil {
			return nil, err
		}
		action, err := toolAction(tool)
		if err != nil {
			return nil, err
		}
		waybill, err := m.reads.TMS.GetWaybill(
			ctx,
			platform.GetWaybillRequest{WaybillID: runContext.WaybillID},
		)
		if err != nil {
			return nil, err
		}
		if err := validateReadBinding(
			action,
			json.RawMessage(call.Arguments),
			runContext,
			waybill,
		); err != nil {
			return nil, err
		}
		return next(ctx, tool, call)
	}
}

func validateReadBinding(
	action domain.Action,
	raw json.RawMessage,
	runContext domain.RunContext,
	waybill platform.Waybill,
) error {
	switch action {
	case domain.ActionGetWaybill:
		var input guardtools.GetWaybillInput
		if err := decodeReadArguments(raw, &input); err != nil {
			return err
		}
		if domain.WaybillID(input.WaybillID) != runContext.WaybillID {
			return fmt.Errorf("get_waybill target does not match the run waybill")
		}
	case domain.ActionGetTracking:
		var input guardtools.GetTrackingInput
		if err := decodeReadArguments(raw, &input); err != nil {
			return err
		}
		if domain.WaybillID(input.WaybillID) != runContext.WaybillID {
			return fmt.Errorf("get_tracking target does not match the run waybill")
		}
	case domain.ActionGetDriver:
		var input guardtools.GetDriverInput
		if err := decodeReadArguments(raw, &input); err != nil {
			return err
		}
		if domain.DriverID(input.DriverID) != waybill.DriverID {
			return fmt.Errorf("get_driver target does not match the run driver")
		}
	case domain.ActionGetRoadWeather:
		var input guardtools.GetRoadWeatherInput
		if err := decodeReadArguments(raw, &input); err != nil {
			return err
		}
		if input.Route != waybill.Origin+"-"+waybill.Destination {
			return fmt.Errorf("get_road_weather target does not match the run route")
		}
	default:
		return fmt.Errorf("unsupported read action %q", action)
	}
	return nil
}

func decodeReadArguments(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode read arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode read arguments: multiple JSON values")
		}
		return fmt.Errorf("decode read arguments: %w", err)
	}
	return nil
}
