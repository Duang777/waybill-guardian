package source

import (
	"fmt"
	"strings"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	guardevents "github.com/Duang777/waybill-guardian/internal/events"
)

const GuardianDelayStreamName = "guardian.delay-projection.v1"

type GuardianTaskBinding struct {
	TaskID           deliverydomain.TaskID
	WaybillID        string
	Partition        string
	Epoch            uint64
	PlannedServiceAt time.Time
}

func MapGuardianDelay(
	record guardevents.Record,
	binding GuardianTaskBinding,
) (deliverydomain.ETADeviationFact, error) {
	if binding.TaskID == "" ||
		strings.TrimSpace(binding.WaybillID) == "" ||
		strings.TrimSpace(binding.Partition) == "" ||
		binding.Epoch == 0 ||
		binding.PlannedServiceAt.IsZero() {
		return deliverydomain.ETADeviationFact{}, fmt.Errorf(
			"guardian delivery task binding is incomplete",
		)
	}
	if strings.TrimSpace(record.Ref.Source) == "" ||
		strings.TrimSpace(record.Ref.ID) == "" ||
		record.SourceVersion <= 0 ||
		record.RecordTime.IsZero() ||
		record.WaybillID == "" {
		return deliverydomain.ETADeviationFact{}, fmt.Errorf(
			"guardian event identity and source position are required",
		)
	}
	if binding.WaybillID != string(record.WaybillID) {
		return deliverydomain.ETADeviationFact{}, fmt.Errorf(
			"guardian event does not match the delivery task binding",
		)
	}
	eventTime, delay, err := guardianDelayProjection(record)
	if err != nil {
		return deliverydomain.ETADeviationFact{}, err
	}
	if eventTime.IsZero() || eventTime.After(record.RecordTime) {
		return deliverydomain.ETADeviationFact{}, fmt.Errorf(
			"guardian event time must not follow record time",
		)
	}
	projectedAt := binding.PlannedServiceAt.UTC().Add(delay)
	return deliverydomain.ETADeviationFact{
		Meta: deliverydomain.OperationalFactHeader{
			SchemaVersion: deliverydomain.OperationalFactSchemaVersion,
			FactID: deliverydomain.FactID(
				record.Ref.Source + ":" + record.Ref.ID,
			),
			Position: deliverydomain.FactPosition{
				Stream: deliverydomain.FactStream{
					SourceSystem: record.Ref.Source,
					Name:         GuardianDelayStreamName,
					Partition:    binding.Partition,
				},
				Epoch:    binding.Epoch,
				Sequence: uint64(record.SourceVersion),
			},
			OccurredAt: eventTime.UTC(),
			ObservedAt: record.RecordTime.UTC(),
		},
		TaskID:             binding.TaskID,
		ProjectedServiceAt: projectedAt,
	}, nil
}

func guardianDelayProjection(
	record guardevents.Record,
) (time.Time, time.Duration, error) {
	switch record.Type {
	case guardevents.DelayDetectedType:
		if record.Detected == nil || record.Correction != nil {
			return time.Time{}, 0, fmt.Errorf(
				"guardian delay event payload does not match its type",
			)
		}
		return delayDuration(
			record.Detected.EventTime,
			record.Detected.StopMinutes,
		)
	case guardevents.DelayCorrectedType:
		if record.Correction == nil || record.Detected != nil {
			return time.Time{}, 0, fmt.Errorf(
				"guardian correction payload does not match its type",
			)
		}
		switch record.Correction.Operation {
		case guardevents.CorrectionRetract:
			if record.Correction.Replacement != nil {
				return time.Time{}, 0, fmt.Errorf(
					"guardian retract correction contains a replacement",
				)
			}
			return record.Correction.EventTime, 0, nil
		case guardevents.CorrectionReplace:
			if record.Correction.Replacement == nil {
				return time.Time{}, 0, fmt.Errorf(
					"guardian replace correction lacks a replacement",
				)
			}
			return delayDuration(
				record.Correction.EventTime,
				record.Correction.Replacement.StopMinutes,
			)
		default:
			return time.Time{}, 0, fmt.Errorf(
				"unsupported guardian correction operation %q",
				record.Correction.Operation,
			)
		}
	default:
		return time.Time{}, 0, fmt.Errorf(
			"guardian event type %q cannot produce a delivery fact",
			record.Type,
		)
	}
}

func delayDuration(
	eventTime time.Time,
	stopMinutes int,
) (time.Time, time.Duration, error) {
	if eventTime.IsZero() || stopMinutes < 0 {
		return time.Time{}, 0, fmt.Errorf("guardian delay projection is invalid")
	}
	const maxDuration = int(^uint(0)>>1) / int(time.Minute)
	if stopMinutes > maxDuration {
		return time.Time{}, 0, fmt.Errorf("guardian delay duration overflows")
	}
	return eventTime, time.Duration(stopMinutes) * time.Minute, nil
}
