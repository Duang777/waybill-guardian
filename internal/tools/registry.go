package tools

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Duang777/waybill-guardian/internal/domain"
	hastekit "github.com/hastekit/agent-sdk-go"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
)

const (
	MetaContractName = "contract_name"
	MetaAccess       = "access"
	AccessRead       = "read"
	AccessWrite      = "write"
)

var ErrCapabilityUnavailable = errors.New("tool capability is unavailable")

type Definition struct {
	Action   domain.Action
	WireName string
	Args     []string
	Access   string
	Tool     agents.Tool
}

type ContractCatalog struct {
	definitions []Definition
	byWireName  map[string]Definition
	byAction    map[domain.Action]Definition
}

type ExecutionRegistry struct {
	catalog          *ContractCatalog
	activeByAction   map[domain.Action]struct{}
	activeByWireName map[string]struct{}
}

type Registry = ExecutionRegistry

func NewRegistry(h *Handlers) (*Registry, error) {
	catalog, err := NewContractCatalog(h)
	if err != nil {
		return nil, err
	}
	actions := make([]domain.Action, 0, len(catalog.definitions))
	for _, definition := range catalog.definitions {
		actions = append(actions, definition.Action)
	}
	return NewExecutionRegistry(catalog, actions)
}

func NewRegistryForActions(
	h *Handlers,
	activeActions []domain.Action,
) (*Registry, error) {
	catalog, err := NewContractCatalog(h)
	if err != nil {
		return nil, err
	}
	return NewExecutionRegistry(catalog, activeActions)
}

func NewContractCatalog(h *Handlers) (*ContractCatalog, error) {
	if h == nil {
		return nil, fmt.Errorf("tool handlers are required")
	}
	definitions := []Definition{
		readDefinition(domain.ActionGetWaybill, "tms_get_waybill", []string{"waybill_id"},
			hastekit.NewTool(h.GetWaybill,
				hastekit.WithName("tms_get_waybill"),
				hastekit.WithDescription("Read waybill details"),
				hastekit.WithReadOnly(true),
				toolMetadata(domain.ActionGetWaybill, AccessRead))),
		readDefinition(domain.ActionGetTracking, "tms_get_tracking", []string{"waybill_id"},
			hastekit.NewTool(h.GetTracking,
				hastekit.WithName("tms_get_tracking"),
				hastekit.WithDescription("Read the ordered tracking points for a waybill"),
				hastekit.WithReadOnly(true),
				toolMetadata(domain.ActionGetTracking, AccessRead))),
		readDefinition(domain.ActionGetDriver, "tms_get_driver", []string{"driver_id"},
			hastekit.NewTool(h.GetDriver,
				hastekit.WithName("tms_get_driver"),
				hastekit.WithDescription("Read driver status and fatigue information"),
				hastekit.WithReadOnly(true),
				toolMetadata(domain.ActionGetDriver, AccessRead))),
		readDefinition(domain.ActionGetRoadWeather, "ext_get_road_weather", []string{"route"},
			hastekit.NewTool(h.GetRoadWeather,
				hastekit.WithName("ext_get_road_weather"),
				hastekit.WithDescription("Read weather and alerts along a route"),
				hastekit.WithReadOnly(true),
				toolMetadata(domain.ActionGetRoadWeather, AccessRead))),
		writeDefinition(domain.ActionReassign, "tms_reassign",
			[]string{"waybill_id", "carrier_id"},
			hastekit.NewTool(h.Reassign,
				hastekit.WithName("tms_reassign"),
				hastekit.WithDescription("Reassign a waybill to an approved carrier"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(true),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionReassign, AccessWrite))),
		writeDefinition(domain.ActionCreateClaim, "tms_create_claim",
			[]string{"waybill_id", "claim_type"},
			hastekit.NewTool(h.CreateClaim,
				hastekit.WithName("tms_create_claim"),
				hastekit.WithDescription("Create a claim for a waybill"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(false),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionCreateClaim, AccessWrite))),
		writeDefinition(domain.ActionSendSMS, "notify_send_sms",
			[]string{"waybill_id", "recipient", "carrier_id"},
			hastekit.NewTool(h.SendSMS,
				hastekit.WithName("notify_send_sms"),
				hastekit.WithDescription("Send an approved waybill notification to a business recipient"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(false),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionSendSMS, AccessWrite))),
	}

	byWireName := make(map[string]Definition, len(definitions))
	byAction := make(map[domain.Action]Definition, len(definitions))
	for _, definition := range definitions {
		if _, exists := byWireName[definition.WireName]; exists {
			return nil, fmt.Errorf("duplicate tool wire name %q", definition.WireName)
		}
		if _, exists := byAction[definition.Action]; exists {
			return nil, fmt.Errorf("duplicate tool action %q", definition.Action)
		}
		byWireName[definition.WireName] = definition
		byAction[definition.Action] = definition
	}
	return &ContractCatalog{
		definitions: definitions,
		byWireName:  byWireName,
		byAction:    byAction,
	}, nil
}

func NewExecutionRegistry(
	catalog *ContractCatalog,
	activeActions []domain.Action,
) (*ExecutionRegistry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("tool contract catalog is required")
	}
	registry := &ExecutionRegistry{
		catalog:          catalog,
		activeByAction:   make(map[domain.Action]struct{}, len(activeActions)),
		activeByWireName: make(map[string]struct{}, len(activeActions)),
	}
	for _, action := range activeActions {
		definition, ok := catalog.byAction[action]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrCapabilityUnavailable, action)
		}
		registry.activeByAction[action] = struct{}{}
		registry.activeByWireName[definition.WireName] = struct{}{}
	}
	return registry, nil
}

func (c *ContractCatalog) Definitions() []Definition {
	return append([]Definition(nil), c.definitions...)
}

func (r *ExecutionRegistry) Definitions() []Definition {
	return r.catalog.Definitions()
}

func (r *ExecutionRegistry) ActiveDefinitions() []Definition {
	result := make([]Definition, 0, len(r.activeByAction))
	for _, definition := range r.catalog.definitions {
		if r.IsActiveAction(definition.Action) {
			result = append(result, definition)
		}
	}
	return result
}

func (r *ExecutionRegistry) Tools() []agents.Tool {
	result := make([]agents.Tool, 0, len(r.catalog.definitions))
	for _, definition := range r.catalog.definitions {
		result = append(result, definition.Tool)
	}
	return result
}

func (c *ContractCatalog) ByWireName(name string) (Definition, bool) {
	definition, ok := c.byWireName[name]
	return definition, ok
}

func (r *ExecutionRegistry) ByWireName(name string) (Definition, bool) {
	return r.catalog.ByWireName(name)
}

func (r *ExecutionRegistry) ActiveByWireName(name string) (Definition, bool) {
	if !r.IsActiveWireName(name) {
		return Definition{}, false
	}
	return r.catalog.ByWireName(name)
}

func (r *ExecutionRegistry) WireName(action domain.Action) (string, bool) {
	definition, ok := r.catalog.byAction[action]
	return definition.WireName, ok
}

func (r *ExecutionRegistry) ActiveWireName(action domain.Action) (string, bool) {
	if !r.IsActiveAction(action) {
		return "", false
	}
	return r.WireName(action)
}

func (r *ExecutionRegistry) IsActiveAction(action domain.Action) bool {
	_, ok := r.activeByAction[action]
	return ok
}

func (r *ExecutionRegistry) IsActiveWireName(name string) bool {
	_, ok := r.activeByWireName[name]
	return ok
}

func readDefinition(action domain.Action, wireName string, args []string, tool agents.Tool) Definition {
	return Definition{Action: action, WireName: wireName, Args: args, Access: AccessRead, Tool: tool}
}

func writeDefinition(action domain.Action, wireName string, args []string, tool agents.Tool) Definition {
	return Definition{Action: action, WireName: wireName, Args: args, Access: AccessWrite, Tool: tool}
}

func toolMetadata(action domain.Action, access string) hastekit.ToolOption {
	return hastekit.WithMeta(map[string]any{
		MetaContractName: string(action),
		MetaAccess:       access,
	})
}

func SortedArgs(args []string) []string {
	result := append([]string(nil), args...)
	sort.Strings(result)
	return result
}
