package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type Capability struct {
	AdapterID               string
	ContractVersion         string
	Actions                 []domain.EffectAction
	SameRequestReplays      bool
	MismatchRejected        bool
	LookupByKey             bool
	KeyRetention            time.Duration
	LookupConsistencyWindow time.Duration
	SupportsRecovery        bool
}

type Binding struct {
	SchemaVersion           string                `json:"schema_version"`
	AdapterID               string                `json:"adapter_id"`
	ContractVersion         string                `json:"contract_version"`
	Action                  domain.EffectAction   `json:"action"`
	Target                  string                `json:"target"`
	Key                     string                `json:"key"`
	Request                 json.RawMessage       `json:"request"`
	RequestDigest           domain.ArtifactDigest `json:"request_digest"`
	CreatedAt               time.Time             `json:"created_at"`
	ExpiresAt               time.Time             `json:"expires_at"`
	LookupConsistencyWindow time.Duration         `json:"lookup_consistency_window"`
}

type BoundRequest struct {
	SchemaVersion    string                `json:"schema_version"`
	Action           domain.EffectAction   `json:"action"`
	Target           string                `json:"target"`
	Parameters       json.RawMessage       `json:"parameters"`
	ParametersDigest domain.ArtifactDigest `json:"parameters_digest"`
}

type Disposition string

const (
	DispositionSucceeded           Disposition = "succeeded"
	DispositionPending             Disposition = "pending"
	DispositionRetryableFailed     Disposition = "retryable_failed"
	DispositionPermanentFailed     Disposition = "permanent_failed"
	DispositionUnknown             Disposition = "unknown"
	DispositionAuthoritativeAbsent Disposition = "authoritative_absent"
)

type Result struct {
	Disposition    Disposition
	ExternalRef    string
	Response       []byte
	ResponseDigest domain.ArtifactDigest
	ErrorCode      string
	RetryAfter     time.Duration
	ObservedAt     time.Time
}

type Adapter interface {
	Capability() Capability
	Bind(domain.EffectPreview, string, time.Time) (Binding, error)
	Dispatch(context.Context, Binding) (Result, error)
	Lookup(context.Context, Binding, time.Time) (Result, error)
}

type Registry struct {
	byAction map[domain.EffectAction]Adapter
}

func NewRegistry(adapters ...Adapter) (*Registry, error) {
	registry := &Registry{byAction: make(map[domain.EffectAction]Adapter)}
	for _, adapter := range adapters {
		if adapter == nil {
			return nil, fmt.Errorf("execution adapter is required")
		}
		capability := adapter.Capability()
		if err := validateCapability(capability); err != nil {
			return nil, err
		}
		for _, action := range capability.Actions {
			if _, exists := registry.byAction[action]; exists {
				return nil, fmt.Errorf("effect action %q has multiple adapters", action)
			}
			registry.byAction[action] = adapter
		}
	}
	return registry, nil
}

func (registry *Registry) BindPreviews(
	effects []domain.EffectPreview,
	recoveryHorizon time.Duration,
) ([]domain.EffectPreview, error) {
	if registry == nil || recoveryHorizon <= 0 {
		return nil, fmt.Errorf("execution registry and recovery horizon are required")
	}
	result := make([]domain.EffectPreview, len(effects))
	copy(result, effects)
	for index := range result {
		adapter, exists := registry.byAction[result[index].Action]
		if !exists {
			return nil, fmt.Errorf(
				"effect action %q has no recoverable adapter",
				result[index].Action,
			)
		}
		capability := adapter.Capability()
		if err := validateCapability(capability); err != nil {
			return nil, err
		}
		if capability.KeyRetention <= recoveryHorizon+
			capability.LookupConsistencyWindow {
			return nil, fmt.Errorf(
				"adapter %q key retention does not cover recovery horizon",
				capability.AdapterID,
			)
		}
		result[index].AdapterID = capability.AdapterID
		result[index].ContractVersion = capability.ContractVersion
		result[index].KeyRetentionSeconds = int64(capability.KeyRetention / time.Second)
		result[index].LookupConsistencyWindowSeconds =
			int64(capability.LookupConsistencyWindow / time.Second)
	}
	return result, nil
}

func (registry *Registry) Adapter(
	action domain.EffectAction,
	adapterID string,
	contractVersion string,
) (Adapter, error) {
	if registry == nil {
		return nil, fmt.Errorf("execution registry is unavailable")
	}
	adapter, exists := registry.byAction[action]
	if !exists {
		return nil, fmt.Errorf("effect action %q has no adapter", action)
	}
	capability := adapter.Capability()
	if capability.AdapterID != adapterID ||
		capability.ContractVersion != contractVersion {
		return nil, fmt.Errorf("effect adapter binding changed")
	}
	return adapter, nil
}

func (registry *Registry) Bind(
	preview domain.EffectPreview,
	key string,
	createdAt time.Time,
) (Binding, error) {
	adapter, err := registry.Adapter(
		preview.Action,
		preview.AdapterID,
		preview.ContractVersion,
	)
	if err != nil {
		return Binding{}, err
	}
	binding, err := adapter.Bind(preview, key, createdAt.UTC())
	if err != nil {
		return Binding{}, err
	}
	if err := verifyBinding(preview, binding, key, createdAt.UTC()); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func NewBinding(
	preview domain.EffectPreview,
	key string,
	createdAt time.Time,
	capability Capability,
) (Binding, error) {
	if key == "" ||
		createdAt.IsZero() ||
		preview.Target == "" ||
		preview.AdapterID != capability.AdapterID ||
		preview.ContractVersion != capability.ContractVersion ||
		preview.KeyRetentionSeconds != int64(capability.KeyRetention/time.Second) ||
		preview.LookupConsistencyWindowSeconds !=
			int64(capability.LookupConsistencyWindow/time.Second) {
		return Binding{}, fmt.Errorf("effect preview does not match adapter capability")
	}
	parameters, err := domain.CanonicalizeJSON(preview.Parameters)
	if err != nil {
		return Binding{}, err
	}
	parametersDigest, err := domain.DigestCanonicalJSON(parameters)
	if err != nil || parametersDigest != preview.ParametersDigest {
		return Binding{}, fmt.Errorf("effect parameters digest mismatch")
	}
	request, err := domain.CanonicalJSON(BoundRequest{
		SchemaVersion:    domain.EffectRequestSchemaVersion,
		Action:           preview.Action,
		Target:           preview.Target,
		Parameters:       json.RawMessage(parameters),
		ParametersDigest: parametersDigest,
	})
	if err != nil {
		return Binding{}, err
	}
	requestDigest, err := domain.DigestCanonicalJSON(request)
	if err != nil {
		return Binding{}, err
	}
	return Binding{
		SchemaVersion:           domain.EffectBindingSchemaVersion,
		AdapterID:               capability.AdapterID,
		ContractVersion:         capability.ContractVersion,
		Action:                  preview.Action,
		Target:                  preview.Target,
		Key:                     key,
		Request:                 json.RawMessage(request),
		RequestDigest:           requestDigest,
		CreatedAt:               createdAt.UTC(),
		ExpiresAt:               createdAt.UTC().Add(capability.KeyRetention),
		LookupConsistencyWindow: capability.LookupConsistencyWindow,
	}, nil
}

func DecodeBinding(raw json.RawMessage) (Binding, error) {
	if !json.Valid(raw) {
		return Binding{}, fmt.Errorf("effect adapter binding is not valid JSON")
	}
	var binding Binding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return Binding{}, fmt.Errorf("decode effect adapter binding: %w", err)
	}
	return binding, nil
}

func VerifyBinding(effect domain.EffectRecord, binding Binding) error {
	preview := domain.EffectPreview{
		ID:                             effect.ID,
		Ordinal:                        effect.Ordinal,
		Action:                         effect.Action,
		Target:                         effect.Target,
		Parameters:                     effect.Parameters,
		ParametersDigest:               effect.ParametersDigest,
		Required:                       effect.Required,
		AdapterID:                      effect.AdapterID,
		ContractVersion:                effect.ContractVersion,
		KeyRetentionSeconds:            int64(effect.KeyExpiresAt.Sub(effect.KeyCreatedAt) / time.Second),
		LookupConsistencyWindowSeconds: effect.LookupConsistencyWindowSeconds,
	}
	if err := verifyBinding(preview, binding, effect.IdempotencyKey, effect.KeyCreatedAt); err != nil {
		return err
	}
	raw, err := domain.CanonicalJSON(binding)
	if err != nil {
		return err
	}
	digest, err := domain.DigestCanonicalJSON(raw)
	if err != nil {
		return err
	}
	if digest != effect.AdapterBindingDigest {
		return fmt.Errorf("effect adapter binding digest changed")
	}
	return nil
}

func verifyBinding(
	preview domain.EffectPreview,
	binding Binding,
	key string,
	createdAt time.Time,
) error {
	if binding.SchemaVersion != domain.EffectBindingSchemaVersion ||
		binding.AdapterID != preview.AdapterID ||
		binding.ContractVersion != preview.ContractVersion ||
		binding.Action != preview.Action ||
		binding.Target != preview.Target ||
		binding.Key != key ||
		!binding.CreatedAt.Equal(createdAt.UTC()) ||
		!binding.ExpiresAt.Equal(
			createdAt.UTC().Add(time.Duration(preview.KeyRetentionSeconds)*time.Second),
		) ||
		binding.LookupConsistencyWindow !=
			time.Duration(preview.LookupConsistencyWindowSeconds)*time.Second {
		return fmt.Errorf("effect adapter returned a mismatched binding")
	}
	canonical, err := domain.CanonicalizeJSON(binding.Request)
	if err != nil {
		return fmt.Errorf("effect adapter request is invalid: %w", err)
	}
	digest, err := domain.DigestCanonicalJSON(canonical)
	if err != nil {
		return err
	}
	if digest != binding.RequestDigest {
		return fmt.Errorf("effect adapter request digest changed")
	}
	var request BoundRequest
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode effect adapter request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("effect adapter request has a trailing value")
	}
	if request.SchemaVersion != domain.EffectRequestSchemaVersion ||
		request.Action != preview.Action ||
		request.Target != preview.Target ||
		request.ParametersDigest != preview.ParametersDigest {
		return fmt.Errorf("effect adapter request changed authoritative fields")
	}
	parametersDigest, err := domain.DigestCanonicalJSON(request.Parameters)
	if err != nil || parametersDigest != preview.ParametersDigest {
		return fmt.Errorf("effect adapter request changed parameters")
	}
	return nil
}

func validateCapability(value Capability) error {
	if value.AdapterID == "" ||
		value.ContractVersion == "" ||
		len(value.Actions) == 0 ||
		!value.SameRequestReplays ||
		!value.MismatchRejected ||
		!value.LookupByKey ||
		value.KeyRetention <= 0 ||
		value.LookupConsistencyWindow < 0 ||
		!value.SupportsRecovery {
		return fmt.Errorf("adapter %q lacks required recovery guarantees", value.AdapterID)
	}
	seen := make(map[domain.EffectAction]struct{}, len(value.Actions))
	for _, action := range value.Actions {
		if action == "" {
			return fmt.Errorf("adapter %q has an empty action", value.AdapterID)
		}
		if _, exists := seen[action]; exists {
			return fmt.Errorf("adapter %q repeats action %q", value.AdapterID, action)
		}
		seen[action] = struct{}{}
	}
	return nil
}
