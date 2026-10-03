package events

import (
	"fmt"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

func TestReduceIsIndependentOfRecordOrder(t *testing.T) {
	detectedV1 := detectedRecord("event-1", 1, 360)
	detectedV2 := detectedRecord("event-2", 2, 300)
	correction := correctionRecord("event-3", 3, detectedV2.Ref, CorrectionReplace, 180)
	records := []Record{detectedV1, detectedV2, correction}

	var expected Projection
	for index, permutation := range permutations(records) {
		projection := Reduce(EventSet{
			Episode: detectedV1.Episode(),
			Records: permutation,
		})
		if index == 0 {
			expected = projection
			continue
		}
		if projection.Fingerprint != expected.Fingerprint {
			t.Fatalf(
				"permutation %d fingerprint = %s, want %s",
				index,
				projection.Fingerprint,
				expected.Fingerprint,
			)
		}
	}
	if expected.State != TransportActive ||
		expected.CurrentRef == nil ||
		*expected.CurrentRef != correction.Ref ||
		expected.CurrentSourceVersion != 3 ||
		expected.Current == nil ||
		expected.Current.StopMinutes != 180 {
		t.Fatalf("projection = %#v", expected)
	}
}

func TestReduceCorrectionBeforeTargetConverges(t *testing.T) {
	target := detectedRecord("event-target", 4, 360)
	correction := correctionRecord("event-correction", 5, target.Ref, CorrectionReplace, 120)

	pending := Reduce(EventSet{
		Episode: correction.Episode(),
		Records: []Record{correction},
	})
	if pending.State != TransportPendingCorrection ||
		pending.ReviewCode != ReviewCorrectionTargetMissing {
		t.Fatalf("pending projection = %#v", pending)
	}

	resolved := Reduce(EventSet{
		Episode: correction.Episode(),
		Records: []Record{correction, target},
	})
	if resolved.State != TransportActive ||
		resolved.CurrentRef == nil ||
		*resolved.CurrentRef != correction.Ref ||
		resolved.Current == nil ||
		resolved.Current.StopMinutes != 120 {
		t.Fatalf("resolved projection = %#v", resolved)
	}
}

func TestReduceRejectsCorrectionTargetFromAnotherEpisode(t *testing.T) {
	target := detectedRecord("event-target", 4, 360)
	target.IncidentKey = "other-incident"
	correction := correctionRecord("event-correction", 5, target.Ref, CorrectionReplace, 120)

	projection := Reduce(EventSet{
		Episode: correction.Episode(),
		Records: []Record{correction},
		CorrectionTargets: map[EventRef]Record{
			target.Ref: target,
		},
	})
	if projection.State != TransportConflicted ||
		projection.ReviewCode != ReviewCorrectionTargetInvalid {
		t.Fatalf("projection = %#v", projection)
	}
}

func TestReduceDetectsSourceVersionConflict(t *testing.T) {
	first := detectedRecord("event-a", 7, 360)
	second := detectedRecord("event-b", 7, 180)
	projection := Reduce(EventSet{
		Episode: first.Episode(),
		Records: []Record{second, first},
	})
	if projection.State != TransportConflicted ||
		projection.ReviewCode != ReviewSourceVersionConflict ||
		projection.ConflictCount != 2 {
		t.Fatalf("projection = %#v", projection)
	}
}

func TestReduceCollapsesEquivalentSourceVersion(t *testing.T) {
	first := detectedRecord("event-b", 7, 360)
	second := detectedRecord("event-a", 7, 360)
	projection := Reduce(EventSet{
		Episode: first.Episode(),
		Records: []Record{first, second},
	})
	if projection.State != TransportActive ||
		projection.CurrentRef == nil ||
		projection.CurrentRef.ID != "event-a" {
		t.Fatalf("projection = %#v", projection)
	}
}

func TestReduceRetraction(t *testing.T) {
	target := detectedRecord("event-target", 4, 360)
	correction := correctionRecord("event-retract", 5, target.Ref, CorrectionRetract, 0)
	projection := Reduce(EventSet{
		Episode: target.Episode(),
		Records: []Record{target, correction},
	})
	if projection.State != TransportRetracted ||
		projection.Current != nil ||
		projection.CurrentRef != nil {
		t.Fatalf("projection = %#v", projection)
	}
	if got := Classify(nil, projection, correction); got != DispositionRetracted {
		t.Fatalf("Classify = %q, want %q", got, DispositionRetracted)
	}
}

func TestClassifyUnchangedProjectionAsStale(t *testing.T) {
	current := Reduce(EventSet{
		Episode: detectedRecord("event-2", 2, 180).Episode(),
		Records: []Record{detectedRecord("event-2", 2, 180)},
	})
	stale := detectedRecord("event-1", 1, 360)
	after := Reduce(EventSet{
		Episode: stale.Episode(),
		Records: []Record{stale, detectedRecord("event-2", 2, 180)},
	})
	if current.Fingerprint != after.Fingerprint {
		t.Fatalf("stale event changed projection: %s != %s", current.Fingerprint, after.Fingerprint)
	}
	if got := Classify(&current, after, stale); got != DispositionStale {
		t.Fatalf("Classify = %q, want %q", got, DispositionStale)
	}
}

func TestReduceBoundsConflictEvidence(t *testing.T) {
	records := make([]Record, 20)
	for index := range records {
		records[index] = detectedRecord(
			fmt.Sprintf("event-%02d", 19-index),
			7,
			index+1,
		)
	}
	projection := Reduce(EventSet{
		Episode: records[0].Episode(),
		Records: records,
	})
	if projection.ConflictCount != 20 || len(projection.ConflictRefs) != maxConflictRefs {
		t.Fatalf(
			"conflict evidence = count %d refs %d",
			projection.ConflictCount,
			len(projection.ConflictRefs),
		)
	}
	for index, ref := range projection.ConflictRefs {
		want := fmt.Sprintf("event-%02d", index)
		if ref.ID != want {
			t.Fatalf("conflict ref %d = %q, want %q", index, ref.ID, want)
		}
	}
}

func detectedRecord(id string, version SourceVersion, stopMinutes int) Record {
	return Record{
		Ref:           EventRef{Source: "urn:tms:region-east", ID: id},
		Type:          DelayDetectedType,
		Subject:       "waybill/YD2026101001",
		DataSchema:    DelayDetectedSchema,
		EnvelopeTime:  time.Date(2026, 10, 10, 12, int(version), 0, 0, time.UTC),
		WaybillID:     domain.WaybillID("YD2026101001"),
		IncidentKey:   "incident-1",
		SourceVersion: version,
		RecordTime:    time.Date(2026, 10, 10, 12, int(version), 0, 0, time.UTC),
		Detected: &DelayDetails{
			EventTime: time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC),
			DelayFields: DelayFields{
				Location:     Location{Code: "MY-N", Name: "Mianyang North"},
				BusinessStep: "transporting",
				ReasonCode:   "stop_duration_exceeded",
				StopMinutes:  stopMinutes,
			},
		},
	}
}

func correctionRecord(
	id string,
	version SourceVersion,
	target EventRef,
	operation CorrectionOperation,
	stopMinutes int,
) Record {
	var replacement *DelayFields
	if operation == CorrectionReplace {
		replacement = &DelayFields{
			Location:     Location{Code: "MY-S", Name: "Mianyang South"},
			BusinessStep: "transporting",
			ReasonCode:   "stop_duration_exceeded",
			StopMinutes:  stopMinutes,
		}
	}
	return Record{
		Ref:           EventRef{Source: "urn:tms:region-east", ID: id},
		Type:          DelayCorrectedType,
		Subject:       "waybill/YD2026101001",
		DataSchema:    DelayCorrectedSchema,
		EnvelopeTime:  time.Date(2026, 10, 10, 12, int(version), 0, 0, time.UTC),
		WaybillID:     domain.WaybillID("YD2026101001"),
		IncidentKey:   "incident-1",
		SourceVersion: version,
		RecordTime:    time.Date(2026, 10, 10, 12, int(version), 0, 0, time.UTC),
		Correction: &DelayCorrection{
			Corrects:    target,
			Operation:   operation,
			EventTime:   time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC),
			Replacement: replacement,
			Reason:      "source correction",
		},
	}
}

func permutations(values []Record) [][]Record {
	var result [][]Record
	var visit func(int)
	current := append([]Record(nil), values...)
	visit = func(index int) {
		if index == len(current) {
			result = append(result, append([]Record(nil), current...))
			return
		}
		for candidate := index; candidate < len(current); candidate++ {
			current[index], current[candidate] = current[candidate], current[index]
			visit(index + 1)
			current[index], current[candidate] = current[candidate], current[index]
		}
	}
	visit(0)
	return result
}
