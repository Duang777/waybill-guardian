package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Duang777/waybill-guardian/internal/platform"
)

var ErrInvalidEffectRequest = errors.New("effect request does not match its authorized identity")

type AuthorizedEffect struct {
	command Command
	request platform.EffectRequest
}

func AuthorizeEffect(
	command Command,
	request platform.EffectRequest,
) (AuthorizedEffect, error) {
	if err := validateCommand(command); err != nil {
		return AuthorizedEffect{}, err
	}
	if !request.Action.IsWrite() ||
		request.Action != command.Identity.Action ||
		request.ArgumentsHash == "" {
		return AuthorizedEffect{}, ErrInvalidEffectRequest
	}
	canonical, err := canonicalArguments(request.Arguments)
	if err != nil {
		return AuthorizedEffect{}, errors.Join(ErrInvalidEffectRequest, err)
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != request.ArgumentsHash {
		return AuthorizedEffect{}, ErrInvalidEffectRequest
	}
	if command.Identity.Version == IdentityLegacyV1 {
		if request.ArgumentsHash != command.Identity.ArgumentsHash &&
			!matchesLegacyArgumentsHash(command, canonical) {
			return AuthorizedEffect{}, ErrInvalidEffectRequest
		}
	} else if request.ArgumentsHash != command.Identity.ArgumentsHash {
		return AuthorizedEffect{}, ErrInvalidEffectRequest
	}
	return AuthorizedEffect{
		command: command,
		request: platform.EffectRequest{
			Action:        request.Action,
			Arguments:     canonical,
			ArgumentsHash: request.ArgumentsHash,
		},
	}, nil
}

func matchesLegacyArgumentsHash(command Command, canonical json.RawMessage) bool {
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &arguments); err != nil || arguments == nil {
		return false
	}
	if _, exists := arguments["idempotency_key"]; exists {
		return false
	}
	key, err := json.Marshal(command.Identity.Key)
	if err != nil {
		return false
	}
	arguments["idempotency_key"] = key
	legacyCanonical, err := json.Marshal(arguments)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(legacyCanonical)
	return hex.EncodeToString(sum[:]) == command.Identity.ArgumentsHash
}

func (effect AuthorizedEffect) Command() Command {
	return effect.command
}

func (effect AuthorizedEffect) Request() platform.EffectRequest {
	request := effect.request
	request.Arguments = append(json.RawMessage(nil), effect.request.Arguments...)
	return request
}

type RecoveryDecision string

const (
	RecoveryResolved         RecoveryDecision = "resolved"
	RecoveryBusy             RecoveryDecision = "busy"
	RecoveryReadyToResume    RecoveryDecision = "ready_to_resume"
	RecoveryPending          RecoveryDecision = "pending"
	RecoveryPermanentFailure RecoveryDecision = "permanent_failure"
	RecoveryManualReview     RecoveryDecision = "manual_review"
)

type RecoveryOutcome struct {
	Decision RecoveryDecision
	Result   Result
	State    State
	RetryAt  time.Time
}
