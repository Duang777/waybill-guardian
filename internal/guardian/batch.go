package guardian

import (
	"context"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

const MaxBatchRuns = 20

var (
	ErrBatchEmpty     = errors.New("batch must contain at least one waybill")
	ErrBatchTooLarge  = errors.New("batch exceeds maximum size")
	ErrBatchDuplicate = errors.New("batch contains a duplicate waybill")
)

type BatchRunResult struct {
	WaybillID domain.WaybillID `json:"waybill_id"`
	Run       *RunView         `json:"run,omitempty"`
	Error     *BatchRunError   `json:"error,omitempty"`
}

type BatchRunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type BatchRunResponse struct {
	Requested int              `json:"requested"`
	Accepted  int              `json:"accepted"`
	Results   []BatchRunResult `json:"results"`
}

func (s *Service) StartBatch(
	ctx context.Context,
	waybillIDs []domain.WaybillID,
) (BatchRunResponse, error) {
	if len(waybillIDs) == 0 {
		return BatchRunResponse{}, ErrBatchEmpty
	}
	if len(waybillIDs) > MaxBatchRuns {
		return BatchRunResponse{}, fmt.Errorf(
			"%w: maximum is %d",
			ErrBatchTooLarge,
			MaxBatchRuns,
		)
	}
	seen := make(map[domain.WaybillID]struct{}, len(waybillIDs))
	for _, id := range waybillIDs {
		if err := domain.ValidateWaybillID(id); err != nil {
			return BatchRunResponse{}, err
		}
		if _, ok := seen[id]; ok {
			return BatchRunResponse{}, fmt.Errorf("%w: %s", ErrBatchDuplicate, id)
		}
		seen[id] = struct{}{}
	}

	result := BatchRunResponse{
		Requested: len(waybillIDs),
		Results:   make([]BatchRunResult, 0, len(waybillIDs)),
	}
	for _, id := range waybillIDs {
		run, err := s.StartRun(ctx, id)
		if err != nil {
			result.Results = append(result.Results, BatchRunResult{
				WaybillID: id,
				Error:     batchRunError(err),
			})
			continue
		}
		result.Accepted++
		runCopy := run
		result.Results = append(result.Results, BatchRunResult{
			WaybillID: id,
			Run:       &runCopy,
		})
	}
	return result, nil
}

func batchRunError(err error) *BatchRunError {
	switch {
	case errors.Is(err, platform.ErrNotFound):
		return &BatchRunError{Code: "not_found", Message: err.Error()}
	case errors.Is(err, ErrRunCapacity):
		return &BatchRunError{Code: "run_capacity_reached", Message: err.Error()}
	case errors.Is(err, ErrServiceClosed):
		return &BatchRunError{Code: "service_closing", Message: "service is shutting down"}
	default:
		return &BatchRunError{Code: "start_failed", Message: "run could not be started"}
	}
}
