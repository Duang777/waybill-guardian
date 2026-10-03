package tools

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
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
	clients, _, err := NewDemoClients()
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
		json.RawMessage(`{"phone":"13800001234","template_id":"delay","params":{"b":"2","a":"1"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.ParseWrite(
		"notify_send_sms",
		json.RawMessage(`{"params":{"a":"1","b":"2"},"template_id":"delay","phone":"13800001234"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != domain.ActionSendSMS || first.Target != "phone/13800001234" {
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

func TestReadToolExecutesDeterministically(t *testing.T) {
	clients, _, err := NewDemoClients()
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
	definition, ok := registry.ByWireName("tms_get_waybill")
	if !ok {
		t.Fatal("tms_get_waybill is not registered")
	}
	call := &agents.ToolCall{FunctionCallMessage: &responses.FunctionCallMessage{
		ID:        "fc-1",
		CallID:    "call-1",
		Name:      definition.WireName,
		Arguments: `{"waybill_id":"YD2026101001"}`,
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
		t.Fatalf("tool output is not deterministic")
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
	clients, _, err := NewDemoClients()
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
