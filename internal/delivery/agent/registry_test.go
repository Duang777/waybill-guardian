package agent

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestRegistryExposesOnlySixNarrowDeliveryTools(t *testing.T) {
	registry, err := NewRegistry(&Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 6 {
		t.Fatalf("definitions = %d, want 6", len(definitions))
	}
	expected := map[string][]string{
		ContractGetProblemSummary:   {"problem_id", "version"},
		ContractRequestOptimization: {"problem_id", "problem_version", "solver_profile"},
		ContractGetCandidate:        {"run_id"},
		ContractCompareRevisions:    {"base_revision_id", "candidate_revision_id"},
		ContractExplainValidation:   {"revision_id"},
		ContractRequestApproval:     {"reason", "revision_id"},
	}
	for _, definition := range definitions {
		descriptor := definition.Tool.GetToolDescriptor()
		function := descriptor.ToolUnion.OfFunction
		if function == nil || function.Strict == nil || !*function.Strict {
			t.Fatalf("tool %q is not strict", definition.ContractName)
		}
		if descriptor.RequiresApproval {
			t.Fatalf("tool %q uses SDK approval instead of delivery approval", definition.ContractName)
		}
		properties, ok := function.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q has no properties", definition.ContractName)
		}
		got := make([]string, 0, len(properties))
		for name := range properties {
			got = append(got, name)
		}
		sort.Strings(got)
		want := append([]string(nil), expected[definition.ContractName]...)
		sort.Strings(want)
		if !sameStrings(got, want) {
			t.Fatalf("tool %q properties = %v, want %v", definition.ContractName, got, want)
		}
		for _, forbidden := range []string{
			"tenant_id",
			"actor",
			"matrix",
			"route",
			"coordinates",
			"constraints",
			"objective_weights",
			"digest",
			"effect_id",
			"idempotency_key",
			"decision",
			"expected_version",
		} {
			if _, exists := properties[forbidden]; exists {
				t.Fatalf("tool %q exposes %q", definition.ContractName, forbidden)
			}
		}
		if descriptor.Meta[metaContractName] != definition.ContractName ||
			descriptor.Meta[metaAccess] != definition.Access {
			t.Fatalf("tool %q metadata = %#v", definition.ContractName, descriptor.Meta)
		}
	}
}

func TestStrictToolRejectsUnknownAndDuplicateAuthorityFields(t *testing.T) {
	registry, err := NewRegistry(&Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.ByWireName("delivery_request_optimization")
	if !ok {
		t.Fatal("optimization tool is missing")
	}
	for _, arguments := range []string{
		`{
			"problem_id":"problem-1",
			"problem_version":1,
			"solver_profile":"default",
			"idempotency_key":"attacker"
		}`,
		`{
			"problem_id":"problem-1",
			"problem_id":"problem-2",
			"problem_version":1,
			"solver_profile":"default"
		}`,
	} {
		_, err := definition.Tool.Execute(t.Context(), deliveryToolCall(arguments))
		if err == nil {
			t.Fatalf("tool accepted authority-bearing input %s", arguments)
		}
	}
}

func TestCommandToolDerivesTenantActorAndIdempotencyKey(t *testing.T) {
	commands := &recordingCommands{}
	handlers := &Handlers{
		commands: commands,
		tenantID: "tenant-a",
		actor:    service.Actor{Subject: "agent:delivery"},
		solverProfiles: map[string]struct{}{
			"production-v1": {},
		},
		approvalTTL: time.Hour,
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.ByWireName("delivery_request_optimization")
	call := deliveryToolCall(`{
		"problem_id":"problem-1",
		"problem_version":3,
		"solver_profile":"production-v1"
	}`)
	call.ThreadID = "thread-1"
	call.CallID = "call-1"
	if _, err := definition.Tool.Execute(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	if commands.optimization.TenantID != "tenant-a" ||
		commands.optimization.Actor.Subject != "agent:delivery" ||
		commands.optimization.ProblemID != "problem-1" ||
		commands.optimization.ProblemVersion != 3 ||
		commands.optimization.IdempotencyKey == "" {
		t.Fatalf("optimization command = %+v", commands.optimization)
	}
	firstKey := commands.optimization.IdempotencyKey
	if _, err := definition.Tool.Execute(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	if commands.optimization.IdempotencyKey != firstKey {
		t.Fatal("same tool call produced a different idempotency key")
	}
}

type recordingCommands struct {
	optimization service.RequestOptimization
}

func (*recordingCommands) CreateProblem(
	context.Context,
	service.CreateProblem,
) (domain.ProblemVersion, service.Replay, error) {
	return domain.ProblemVersion{}, service.Replay{}, errors.New("not used")
}

func (commands *recordingCommands) RequestOptimization(
	_ context.Context,
	request service.RequestOptimization,
) (domain.OptimizationRun, service.Replay, error) {
	commands.optimization = request
	return domain.OptimizationRun{
		TenantID:       request.TenantID,
		ID:             "run-1",
		ProblemID:      request.ProblemID,
		ProblemVersion: request.ProblemVersion,
		Status:         domain.RunQueued,
		Version:        1,
	}, service.Replay{}, nil
}

func (*recordingCommands) CancelOptimization(
	context.Context,
	service.CancelOptimization,
) (domain.OptimizationRun, error) {
	return domain.OptimizationRun{}, errors.New("not used")
}

func (*recordingCommands) RequestPlanApproval(
	context.Context,
	service.RequestPlanApproval,
) (domain.PlanApproval, service.Replay, error) {
	return domain.PlanApproval{}, service.Replay{}, errors.New("not used")
}

func (*recordingCommands) DecideApproval(
	context.Context,
	service.DecideApproval,
) (domain.DispatchExecution, service.Replay, error) {
	return domain.DispatchExecution{}, service.Replay{}, errors.New("not used")
}

func deliveryToolCall(arguments string) *agents.ToolCall {
	return &agents.ToolCall{
		FunctionCallMessage: &responses.FunctionCallMessage{
			ID:        "item-1",
			CallID:    "call-1",
			Name:      "delivery_request_optimization",
			Arguments: arguments,
		},
	}
}

func sameStrings(left, right []string) bool {
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

var _ service.Commands = (*recordingCommands)(nil)
