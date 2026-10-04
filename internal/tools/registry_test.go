package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
	"gopkg.in/yaml.v3"
)

type contractEntry struct {
	Args []string `yaml:"args"`
}

type toolContract struct {
	Read  map[string]contractEntry `yaml:"read"`
	Write map[string]contractEntry `yaml:"write"`
}

func TestRegistryMatchesContract(t *testing.T) {
	raw, err := os.ReadFile("../../contract.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract toolContract
	if err := yaml.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	clients, _, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != len(contract.Read)+len(contract.Write) {
		t.Fatalf("registered %d tools, contract has %d", len(definitions), len(contract.Read)+len(contract.Write))
	}

	seen := make(map[string]bool)
	for _, definition := range definitions {
		name := string(definition.Action)
		if seen[name] {
			t.Fatalf("duplicate contract tool %q", name)
		}
		seen[name] = true
		entry, isRead := contract.Read[name]
		if !isRead {
			entry = contract.Write[name]
		}
		if entry.Args == nil {
			t.Fatalf("tool %q is not in contract", name)
		}
		descriptor := definition.Tool.GetToolDescriptor()
		if descriptor.ToolUnion.OfFunction == nil {
			t.Fatalf("tool %q has no function descriptor", name)
		}
		if descriptor.ToolUnion.OfFunction.Name != definition.WireName {
			t.Fatalf("tool %q wire name = %q", name, descriptor.ToolUnion.OfFunction.Name)
		}
		required := schemaRequired(t, descriptor.ToolUnion.OfFunction.Parameters)
		if !sameStrings(required, entry.Args) {
			t.Fatalf("tool %q required fields = %v, contract args = %v", name, required, entry.Args)
		}
		if isRead {
			if descriptor.RequiresApproval {
				t.Fatalf("read tool %q requires approval", name)
			}
			if descriptor.Annotations == nil || descriptor.Annotations.ReadOnlyHint == nil ||
				!*descriptor.Annotations.ReadOnlyHint {
				t.Fatalf("read tool %q lacks read-only annotation", name)
			}
		} else {
			if !descriptor.RequiresApproval {
				t.Fatalf("write tool %q does not require approval", name)
			}
			if descriptor.Annotations == nil || descriptor.Annotations.IdempotentHint == nil ||
				!*descriptor.Annotations.IdempotentHint {
				t.Fatalf("write tool %q lacks idempotent annotation", name)
			}
			properties, _ := descriptor.ToolUnion.OfFunction.Parameters["properties"].(map[string]any)
			if _, exists := properties["idempotency_key"]; exists {
				t.Fatalf("write tool %q exposes idempotency_key", name)
			}
			if _, exists := properties["effect_id"]; exists {
				t.Fatalf("write tool %q exposes effect_id", name)
			}
		}
		if descriptor.Meta[MetaContractName] != name || descriptor.Meta[MetaAccess] != definition.Access {
			t.Fatalf("tool %q metadata = %#v", name, descriptor.Meta)
		}
	}
}

func TestParseWriteCanonicalizesBusinessArguments(t *testing.T) {
	registry := testRegistry(t)
	first, err := registry.ParseWrite(
		"notify_send_sms",
		json.RawMessage(
			`{"waybill_id":"YD2026101001","recipient":"shipper","carrier_id":"CARRIER-SW-42"}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.ParseWrite(
		"notify_send_sms",
		json.RawMessage(
			`{"carrier_id":"CARRIER-SW-42","recipient":"shipper","waybill_id":"YD2026101001"}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != domain.ActionSendSMS ||
		first.WaybillID != "YD2026101001" ||
		first.Target != "waybill/YD2026101001/recipient/shipper" {
		t.Fatalf("canonical write = %+v", first)
	}
	if string(first.Arguments) != string(second.Arguments) ||
		first.ArgumentsHash != second.ArgumentsHash {
		t.Fatalf("canonical writes differ:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if first.LegacyKey != "" {
		t.Fatalf("business-only write has legacy key %q", first.LegacyKey)
	}
}

func TestCanonicalWriteRejectsAnotherRunWaybill(t *testing.T) {
	registry := testRegistry(t)
	write, err := registry.ParseWrite(
		"notify_send_sms",
		json.RawMessage(
			`{"waybill_id":"YD2026101001","recipient":"shipper","carrier_id":"CARRIER-SW-42"}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.ValidateRunContext(domain.RunContext{
		WaybillID: "YD2026101002",
	}); err == nil {
		t.Fatal("write for another waybill was accepted")
	}
	if err := write.ValidateRunContext(domain.RunContext{
		WaybillID: "YD2026101001",
	}); err != nil {
		t.Fatalf("write for run waybill was rejected: %v", err)
	}
}

func TestParseWriteRejectsUnknownExecutionFields(t *testing.T) {
	registry := testRegistry(t)
	for _, field := range []string{"effect_id", "unknown"} {
		raw := `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42","` +
			field + `":"forged"}`
		if _, err := registry.ParseWrite("tms_reassign", json.RawMessage(raw)); err == nil ||
			!strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("field %q error = %v", field, err)
		}
	}

	legacy, err := registry.ParseWrite(
		"tms_reassign",
		json.RawMessage(
			`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42","idempotency_key":"old-key"}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.LegacyKey != "old-key" || strings.Contains(string(legacy.Arguments), "idempotency_key") {
		t.Fatalf("legacy canonical write = %+v", legacy)
	}
}

func TestExecutionRegistrySeparatesActiveAndHistoricalTools(t *testing.T) {
	clients, _, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	active := []domain.Action{
		domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather,
		domain.ActionReassign,
	}
	registry, err := NewRegistryForActions(handlers, active)
	if err != nil {
		t.Fatal(err)
	}

	if len(registry.Definitions()) != 7 || len(registry.Tools()) != 7 {
		t.Fatalf(
			"execution registry = definitions:%d tools:%d, want 7 each",
			len(registry.Definitions()),
			len(registry.Tools()),
		)
	}
	if len(registry.ActiveDefinitions()) != len(active) {
		t.Fatalf("active definitions = %d, want %d", len(registry.ActiveDefinitions()), len(active))
	}
	if !registry.IsActiveAction(domain.ActionReassign) ||
		registry.IsActiveAction(domain.ActionCreateClaim) ||
		registry.IsActiveWireName("notify_send_sms") {
		t.Fatalf("unexpected active actions: %+v", registry.ActiveDefinitions())
	}

	claim := json.RawMessage(`{"waybill_id":"YD2026101001","claim_type":"damage"}`)
	if _, err := registry.ParseWrite("tms_create_claim", claim); err != nil {
		t.Fatalf("historical parser rejected catalog tool: %v", err)
	}
	if _, err := registry.ParseActiveWrite("tms_create_claim", claim); !errors.Is(
		err,
		ErrCapabilityUnavailable,
	) {
		t.Fatalf("inactive write error = %v, want ErrCapabilityUnavailable", err)
	}
}

func TestExecutionRegistryRejectsUnknownActiveAction(t *testing.T) {
	clients, _, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRegistryForActions(handlers, []domain.Action{"tms.unknown"})
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("error = %v, want ErrCapabilityUnavailable", err)
	}
}

func TestReadToolsReturnAllowlistedEvidence(t *testing.T) {
	clients, _, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct {
		wireName  string
		arguments string
	}{
		{"tms_get_waybill", `{"waybill_id":"YD2026101001"}`},
		{"tms_get_tracking", `{"waybill_id":"YD2026101001"}`},
		{"tms_get_driver", `{"driver_id":"DRV-0286"}`},
		{"ext_get_road_weather", `{"route":"杭州-成都"}`},
	}
	var transcript strings.Builder
	for index, item := range calls {
		definition, ok := registry.ByWireName(item.wireName)
		if !ok {
			t.Fatalf("%s is not registered", item.wireName)
		}
		call := &agents.ToolCall{FunctionCallMessage: &responses.FunctionCallMessage{
			ID:        fmt.Sprintf("fc-%d", index),
			CallID:    fmt.Sprintf("call-%d", index),
			Name:      definition.WireName,
			Arguments: item.arguments,
		}}
		first, err := definition.Tool.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		second, err := definition.Tool.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if *first.Output.OfString != *second.Output.OfString {
			t.Fatalf("%s output is not deterministic", item.wireName)
		}
		transcript.WriteString(*first.Output.OfString)
	}

	output := transcript.String()
	for _, expected := range []string{
		"YD2026101001",
		"CARRIER-SW-42",
		"绵阳北服务区",
		`"stop_hours":6`,
		`"continuous_drive_hours":9`,
		`"fatigue_alert":true`,
		`"alert_level":"none"`,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("tool output does not contain operational evidence %q: %s", expected, output)
		}
	}
	for _, prohibited := range []string{
		"13800001234",
		"13961234567",
		"川A8X6Q2",
		`"shipper_phone"`,
		`"phone"`,
		`"plate"`,
		`"longitude"`,
		`"latitude"`,
		"120.1551",
		"30.2741",
	} {
		if strings.Contains(output, prohibited) {
			t.Errorf("tool output contains prohibited value %q: %s", prohibited, output)
		}
	}
}

func TestFixtureRuntimeValidatesSMSBusinessReferences(t *testing.T) {
	_, runtime, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}

	for index, input := range []SendSMSInput{
		{
			WaybillID: "YD2026101001",
			Recipient: RecipientShipper,
			CarrierID: "CARRIER-SW-42",
		},
		{
			WaybillID: "YD2026101001",
			Recipient: RecipientDriver,
			CarrierID: "CARRIER-SW-42",
		},
	} {
		arguments, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		argumentsHash, err := idempotency.ArgumentsHash(string(arguments))
		if err != nil {
			t.Fatal(err)
		}
		request := platform.EffectRequest{
			Action:        domain.ActionSendSMS,
			Arguments:     arguments,
			ArgumentsHash: argumentsHash,
		}
		key := domain.IdempotencyKey(fmt.Sprintf("sms-key-%d", index))
		binding, err := runtime.Bind(request, key, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		result := runtime.Dispatch(context.Background(), binding, request, key)
		if result.Disposition != platform.EffectSucceeded {
			t.Fatalf("SMS dispatch = %+v", result)
		}
	}
	if runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf(
			"SMS writes = %d, want 2",
			runtime.WriteCount(domain.ActionSendSMS),
		)
	}
}

func schemaRequired(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var normalized struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	sort.Strings(normalized.Required)
	return normalized.Required
}

func sameStrings(left, right []string) bool {
	left = SortedArgs(left)
	right = SortedArgs(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	clients, _, err := NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
