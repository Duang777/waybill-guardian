package tools

import (
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

type Definition struct {
	Action   domain.Action
	WireName string
	Args     []string
	Access   string
	Tool     agents.Tool
}

type Registry struct {
	definitions []Definition
	byWireName  map[string]Definition
}

func NewRegistry(h *Handlers) (*Registry, error) {
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
			[]string{"waybill_id", "carrier_id", "idempotency_key"},
			hastekit.NewTool(h.Reassign,
				hastekit.WithName("tms_reassign"),
				hastekit.WithDescription("Reassign a waybill to an approved carrier"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(true),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionReassign, AccessWrite))),
		writeDefinition(domain.ActionCreateClaim, "tms_create_claim",
			[]string{"waybill_id", "claim_type", "idempotency_key"},
			hastekit.NewTool(h.CreateClaim,
				hastekit.WithName("tms_create_claim"),
				hastekit.WithDescription("Create a claim for a waybill"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(false),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionCreateClaim, AccessWrite))),
		writeDefinition(domain.ActionSendSMS, "notify_send_sms",
			[]string{"phone", "template_id", "params", "idempotency_key"},
			hastekit.NewTool(h.SendSMS,
				hastekit.WithName("notify_send_sms"),
				hastekit.WithDescription("Send an approved SMS notification"),
				hastekit.WithNeedsApproval(true),
				hastekit.WithDestructive(false),
				hastekit.WithIdempotent(true),
				toolMetadata(domain.ActionSendSMS, AccessWrite))),
	}

	byWireName := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		if _, exists := byWireName[definition.WireName]; exists {
			return nil, fmt.Errorf("duplicate tool wire name %q", definition.WireName)
		}
		byWireName[definition.WireName] = definition
	}
	return &Registry{definitions: definitions, byWireName: byWireName}, nil
}

func (r *Registry) Definitions() []Definition {
	return append([]Definition(nil), r.definitions...)
}

func (r *Registry) Tools() []agents.Tool {
	result := make([]agents.Tool, 0, len(r.definitions))
	for _, definition := range r.definitions {
		result = append(result, definition.Tool)
	}
	return result
}

func (r *Registry) ByWireName(name string) (Definition, bool) {
	definition, ok := r.byWireName[name]
	return definition, ok
}

func (r *Registry) WireName(action domain.Action) (string, bool) {
	for _, definition := range r.definitions {
		if definition.Action == action {
			return definition.WireName, true
		}
	}
	return "", false
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
