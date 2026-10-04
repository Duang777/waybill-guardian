package platform

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

type WriteCapability struct {
	Action                   domain.Action
	AdapterID                string
	ContractVersion          string
	Environment              string
	KeyScope                 string
	KeyRetention             time.Duration
	LookupConsistencyWindow  time.Duration
	SameRequestReplays       bool
	MismatchedRequestRejects bool
	LookupByKey              bool
}

type EffectRequest struct {
	Action        domain.Action
	Arguments     json.RawMessage
	ArgumentsHash string
}

type EffectBinding struct {
	SchemaVersion           int
	Action                  domain.Action
	AdapterID               string
	ContractVersion         string
	ProviderOperation       string
	ProviderScopeDigest     string
	ProviderRequestHash     string
	KeyCreatedAt            time.Time
	KeyExpiresAt            time.Time
	LookupConsistencyWindow time.Duration
}

type DispatchResult struct {
	Disposition       EffectDisposition
	Response          json.RawMessage
	ExternalRef       string
	ExternalRequestID string
	ResponseDigest    string
	ErrorCode         string
	RetryAfter        time.Duration
}

type LookupDisposition string

const (
	LookupApplied  LookupDisposition = "applied"
	LookupRejected LookupDisposition = "rejected"
	LookupAbsent   LookupDisposition = "authoritative_absent"
	LookupPending  LookupDisposition = "pending"
	LookupConflict LookupDisposition = "conflict"
)

type LookupResult struct {
	Disposition       LookupDisposition
	Response          json.RawMessage
	ExternalRef       string
	ExternalRequestID string
	ResponseDigest    string
	ErrorCode         string
	RetryAfter        time.Duration
}

type WriteRuntime interface {
	AdvertisedActions() []domain.Action
	Bind(EffectRequest, domain.IdempotencyKey, time.Time) (EffectBinding, error)
	Dispatch(context.Context, EffectBinding, EffectRequest, domain.IdempotencyKey) DispatchResult
	Lookup(context.Context, EffectBinding, domain.IdempotencyKey) LookupResult
	SupportsRecovery(EffectBinding) bool
}
