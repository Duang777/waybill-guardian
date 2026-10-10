package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (application *Application) ResolveExecutionReservation(
	ctx context.Context,
	request ResolveExecutionReservation,
) (domain.DispatchExecution, Replay, error) {
	if err := application.checkOpen(); err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	reason := strings.TrimSpace(request.Reason)
	compensationReference := strings.TrimSpace(request.CompensationReference)
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.IdempotencyKey == "" ||
		request.ExecutionID == "" ||
		request.ExpectedVersion == 0 ||
		reason == "" ||
		len(reason) > 1_000 ||
		len(compensationReference) > 1_000 {
		return domain.DispatchExecution{}, Replay{},
			fmt.Errorf("execution reservation resolution is incomplete")
	}
	requestDigest, err := domain.Digest(struct {
		Operation             string
		TenantID              domain.TenantID
		ExecutionID           domain.ExecutionID
		ExpectedVersion       uint64
		Reason                string
		CompensationReference string
	}{
		Operation:             "delivery.resolve_execution_reservation.v1",
		TenantID:              request.TenantID,
		ExecutionID:           request.ExecutionID,
		ExpectedVersion:       request.ExpectedVersion,
		Reason:                reason,
		CompensationReference: compensationReference,
	})
	if err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	return application.store.ResolveExecutionReservation(
		ctx,
		ResolveExecutionReservationTx{
			TenantID:              request.TenantID,
			IdempotencyKey:        request.IdempotencyKey,
			RequestDigest:         requestDigest,
			Actor:                 request.Actor,
			ExecutionID:           request.ExecutionID,
			ExpectedVersion:       request.ExpectedVersion,
			Reason:                reason,
			CompensationReference: compensationReference,
			Now:                   application.clock().UTC(),
		},
	)
}
