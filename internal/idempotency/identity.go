package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/google/uuid"
)

type IdentityVersion string

const (
	IdentityLegacyV1 IdentityVersion = "legacy-v1"
	IdentityEffectV1 IdentityVersion = "effect-v1"
)

var (
	ErrInvalidIdentity          = errors.New("invalid effect identity")
	ErrExecutionIdentityMissing = errors.New("execution identity is missing")

	proposalNamespace = uuid.MustParse("46eb81ef-2e6d-5ba6-8b9a-f72c924d8959")
	effectNamespace   = uuid.MustParse("ea50d3aa-af21-5bb3-9bfd-6f35a36ce827")
)

type DerivationInput struct {
	RunContext domain.RunContext
	Action     domain.Action
	Target     string
	Arguments  json.RawMessage
}

type Identity struct {
	Version       IdentityVersion
	EffectID      domain.EffectID
	Key           domain.IdempotencyKey
	Action        domain.Action
	ArgumentsHash string
}

type proposalSeed struct {
	RunID       domain.RunID      `json:"run_id"`
	IncidentID  domain.IncidentID `json:"incident_id"`
	WaybillID   domain.WaybillID  `json:"waybill_id"`
	PlanVersion int               `json:"plan_version"`
}

type proposalItemSeed struct {
	IdentityVersion IdentityVersion `json:"identity_version"`
	Action          domain.Action   `json:"action"`
	Target          string          `json:"target"`
	ArgumentsHash   string          `json:"arguments_hash"`
}

type effectSeed struct {
	ProposalID     string `json:"proposal_id"`
	ProposalItemID string `json:"proposal_item_id"`
}

func Derive(input DerivationInput) (Identity, error) {
	if input.RunContext.RunID == "" ||
		input.RunContext.IncidentID == "" ||
		input.RunContext.WaybillID == "" ||
		input.RunContext.PlanVersion <= 0 ||
		!input.Action.IsWrite() ||
		input.Target == "" ||
		len(input.Arguments) == 0 {
		return Identity{}, ErrInvalidIdentity
	}

	argumentsHash, err := ArgumentsHash(string(input.Arguments))
	if err != nil {
		return Identity{}, err
	}
	proposalRaw, err := json.Marshal(proposalSeed{
		RunID:       input.RunContext.RunID,
		IncidentID:  input.RunContext.IncidentID,
		WaybillID:   input.RunContext.WaybillID,
		PlanVersion: input.RunContext.PlanVersion,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("marshal proposal identity: %w", err)
	}
	proposalID := uuid.NewSHA1(proposalNamespace, proposalRaw).String()

	itemRaw, err := json.Marshal(proposalItemSeed{
		IdentityVersion: IdentityEffectV1,
		Action:          input.Action,
		Target:          input.Target,
		ArgumentsHash:   argumentsHash,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("marshal proposal item identity: %w", err)
	}
	itemSum := sha256.Sum256(itemRaw)
	proposalItemID := hex.EncodeToString(itemSum[:])

	effectRaw, err := json.Marshal(effectSeed{
		ProposalID:     proposalID,
		ProposalItemID: proposalItemID,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("marshal effect identity: %w", err)
	}
	effectID := domain.EffectID(uuid.NewSHA1(effectNamespace, effectRaw).String())

	return Identity{
		Version:       IdentityEffectV1,
		EffectID:      effectID,
		Key:           KeyForEffect(effectID),
		Action:        input.Action,
		ArgumentsHash: argumentsHash,
	}, nil
}

func LegacyIdentity(
	action domain.Action,
	key domain.IdempotencyKey,
	argumentsHash string,
) (Identity, error) {
	if !action.IsWrite() || key == "" || argumentsHash == "" {
		return Identity{}, ErrInvalidIdentity
	}
	return Identity{
		Version:       IdentityLegacyV1,
		EffectID:      legacyEffectID(key),
		Key:           key,
		Action:        action,
		ArgumentsHash: argumentsHash,
	}, nil
}

func KeyForEffect(effectID domain.EffectID) domain.IdempotencyKey {
	sum := sha256.Sum256([]byte("waybill-effect-key-v1\x00" + string(effectID)))
	return domain.IdempotencyKey(hex.EncodeToString(sum[:]))
}

func (identity Identity) Validate() error {
	if identity.EffectID == "" ||
		identity.Key == "" ||
		!identity.Action.IsWrite() ||
		identity.ArgumentsHash == "" {
		return ErrInvalidIdentity
	}
	switch identity.Version {
	case IdentityEffectV1:
		if identity.Key != KeyForEffect(identity.EffectID) {
			return ErrInvalidIdentity
		}
	case IdentityLegacyV1:
		if identity.EffectID != legacyEffectID(identity.Key) {
			return ErrInvalidIdentity
		}
	default:
		return ErrInvalidIdentity
	}
	return nil
}

func legacyEffectID(key domain.IdempotencyKey) domain.EffectID {
	sum := sha256.Sum256([]byte(key))
	return domain.EffectID("legacy-" + hex.EncodeToString(sum[:]))
}

type executionContextKey struct{}

func WithExecution(ctx context.Context, identity Identity) (context.Context, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, executionContextKey{}, identity), nil
}

func ExecutionFromContext(ctx context.Context) (Identity, error) {
	identity, ok := ctx.Value(executionContextKey{}).(Identity)
	if !ok {
		return Identity{}, ErrExecutionIdentityMissing
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}
