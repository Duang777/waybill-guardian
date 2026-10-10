package source

import (
	"testing"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	guarddomain "github.com/Duang777/waybill-guardian/internal/domain"
	guardevents "github.com/Duang777/waybill-guardian/internal/events"
)

func TestMapGuardianDelayUsesAuthenticatedSourcePosition(t *testing.T) {
	recordTime := time.Date(2026, time.October, 10, 12, 30, 0, 0, time.UTC)
	eventTime := recordTime.Add(-time.Minute)
	record := guardevents.Record{
		Ref: guardevents.EventRef{
			Source: "urn:tms:region-east",
			ID:     "evt-delay-41",
		},
		Type:          guardevents.DelayDetectedType,
		WaybillID:     guarddomain.WaybillID("YD2026101001"),
		SourceVersion: 41,
		RecordTime:    recordTime,
		Detected: &guardevents.DelayDetails{
			EventTime: eventTime,
			DelayFields: guardevents.DelayFields{
				StopMinutes: 30,
			},
		},
	}
	plannedAt := recordTime.Add(time.Hour)
	fact, err := MapGuardianDelay(record, GuardianTaskBinding{
		TaskID:           "delivery-1",
		WaybillID:        "YD2026101001",
		Partition:        "YD2026101001",
		Epoch:            3,
		PlannedServiceAt: plannedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fact.Meta.Position != (deliverydomain.FactPosition{
		Stream: deliverydomain.FactStream{
			SourceSystem: "urn:tms:region-east",
			Name:         GuardianDelayStreamName,
			Partition:    "YD2026101001",
		},
		Epoch:    3,
		Sequence: 41,
	}) ||
		fact.Meta.FactID != "urn:tms:region-east:evt-delay-41" ||
		!fact.Meta.OccurredAt.Equal(eventTime) ||
		!fact.Meta.ObservedAt.Equal(recordTime) ||
		!fact.ProjectedServiceAt.Equal(plannedAt.Add(30*time.Minute)) {
		t.Fatalf("mapped fact = %+v", fact)
	}
	if _, err := deliverydomain.BuildLedgerFact(fact); err != nil {
		t.Fatalf("mapped fact violates delivery ledger contract: %v", err)
	}
}

func TestMapGuardianCorrectionProjectsReplaceAndRetract(t *testing.T) {
	recordTime := time.Date(2026, time.October, 10, 12, 45, 0, 0, time.UTC)
	eventTime := recordTime.Add(-time.Minute)
	binding := GuardianTaskBinding{
		TaskID:           "delivery-1",
		WaybillID:        "YD2026101001",
		Partition:        "YD2026101001",
		Epoch:            1,
		PlannedServiceAt: recordTime.Add(time.Hour),
	}
	record := guardevents.Record{
		Ref: guardevents.EventRef{
			Source: "urn:tms:region-east",
			ID:     "evt-correction-42",
		},
		Type:          guardevents.DelayCorrectedType,
		WaybillID:     guarddomain.WaybillID("YD2026101001"),
		SourceVersion: 42,
		RecordTime:    recordTime,
		Correction: &guardevents.DelayCorrection{
			Operation: guardevents.CorrectionReplace,
			EventTime: eventTime,
			Replacement: &guardevents.DelayFields{
				StopMinutes: 15,
			},
		},
	}
	replaced, err := MapGuardianDelay(record, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced.ProjectedServiceAt.Equal(
		binding.PlannedServiceAt.Add(15 * time.Minute),
	) {
		t.Fatalf("replace projection = %s", replaced.ProjectedServiceAt)
	}

	record.Ref.ID = "evt-retract-43"
	record.SourceVersion = 43
	record.Correction.Operation = guardevents.CorrectionRetract
	record.Correction.Replacement = nil
	retracted, err := MapGuardianDelay(record, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !retracted.ProjectedServiceAt.Equal(binding.PlannedServiceAt) {
		t.Fatalf("retract projection = %s", retracted.ProjectedServiceAt)
	}
}

func TestMapGuardianDelayRejectsUnsequencedOrMismatchedEvents(t *testing.T) {
	at := time.Date(2026, time.October, 10, 12, 30, 0, 0, time.UTC)
	validBinding := GuardianTaskBinding{
		TaskID:           "delivery-1",
		WaybillID:        "YD2026101001",
		Partition:        "YD2026101001",
		Epoch:            1,
		PlannedServiceAt: at.Add(time.Hour),
	}
	tests := []guardevents.Record{
		{
			Ref:           guardevents.EventRef{Source: "urn:tms:east", ID: "event-1"},
			Type:          guardevents.DelayDetectedType,
			WaybillID:     "YD2026101001",
			SourceVersion: 0,
			RecordTime:    at,
			Detected:      &guardevents.DelayDetails{EventTime: at},
		},
		{
			Ref:           guardevents.EventRef{Source: "urn:tms:east", ID: "event-1"},
			Type:          guardevents.DelayDetectedType,
			WaybillID:     "YD2026101001",
			SourceVersion: 1,
			RecordTime:    at,
			Correction: &guardevents.DelayCorrection{
				Operation: guardevents.CorrectionRetract,
				EventTime: at,
			},
		},
	}
	for index, record := range tests {
		if _, err := MapGuardianDelay(record, validBinding); err == nil {
			t.Fatalf("invalid guardian event %d was accepted", index)
		}
	}
	valid := tests[0]
	valid.SourceVersion = 1
	wrongBinding := validBinding
	wrongBinding.WaybillID = "YD2026101002"
	if _, err := MapGuardianDelay(valid, wrongBinding); err == nil {
		t.Fatal("guardian event was mapped to another waybill's task")
	}
}
