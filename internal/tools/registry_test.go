package tools

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

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
		}
		if descriptor.Meta[MetaContractName] != name || descriptor.Meta[MetaAccess] != definition.Access {
			t.Fatalf("tool %q metadata = %#v", name, descriptor.Meta)
		}
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
