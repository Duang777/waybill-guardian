package events

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

var decodeNow = time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)

func TestDecodeStructuredDetectionCanonicalizesEquivalentJSON(t *testing.T) {
	first := decodeEvent(t, validDetectionEvent())
	second := decodeEvent(t, `{
		"data": {
			"observations": {"stop_minutes": 360},
			"reason_code": "stop_duration_exceeded",
			"business_step": "transporting",
			"location": {"name": "Mianyang North Service Area", "code": "MY-N-SERVICE"},
			"record_time": "2026-10-10T20:30:00+08:00",
			"event_time": "2026-10-10T20:28:31+08:00",
			"source_version": 41,
			"incident_key": "delay-20261010-000184",
			"waybill_id": "YD2026101001"
		},
		"dataschema": "urn:waybill-guardian:schema:delay-detected:v1",
		"datacontenttype": "application/json",
		"time": "2026-10-10T20:30:00+08:00",
		"subject": "waybill/YD2026101001",
		"type": "com.waybill.tracking.delay.detected.v1",
		"source": "urn:tms:region-east",
		"id": "evt-20261010-000184",
		"specversion": "1.0"
	}`)

	if first.EventHash() != second.EventHash() {
		t.Fatalf("equivalent event hashes differ: %s != %s", first.EventHash(), second.EventHash())
	}
	if first.DataHash() != second.DataHash() {
		t.Fatalf("equivalent data hashes differ: %s != %s", first.DataHash(), second.DataHash())
	}
	if !bytes.Equal(first.CanonicalEvent(), second.CanonicalEvent()) {
		t.Fatalf(
			"equivalent canonical events differ:\n%s\n%s",
			first.CanonicalEvent(),
			second.CanonicalEvent(),
		)
	}

	record := first.Record()
	if record.Ref.ID != "evt-20261010-000184" ||
		record.Ref.Source != "urn:tms:region-east" ||
		record.WaybillID != "YD2026101001" ||
		record.SourceVersion != 41 ||
		record.Detected == nil ||
		record.Detected.StopMinutes != 360 {
		t.Fatalf("decoded record = %#v", record)
	}
	if got := string(first.CanonicalEvent()); !strings.Contains(
		got,
		`"time":"2026-10-10T12:30:00Z"`,
	) {
		t.Fatalf("canonical event time is not UTC: %s", got)
	}
}

func TestDecodeStructuredCorrection(t *testing.T) {
	submission := decodeEvent(t, validCorrectionEvent())
	record := submission.Record()
	if record.Type != DelayCorrectedType || record.Correction == nil {
		t.Fatalf("decoded record = %#v", record)
	}
	if record.Correction.Operation != CorrectionReplace ||
		record.Correction.Corrects.ID != "evt-20261010-000184" ||
		record.Correction.Replacement == nil ||
		record.Correction.Replacement.StopMinutes != 180 {
		t.Fatalf("decoded correction = %#v", record.Correction)
	}
}

func TestDecodeStructuredRejectsInvalidRequests(t *testing.T) {
	oversized := strings.Repeat(" ", int(MaxBodyBytes)+1)
	tests := []struct {
		name          string
		contentType   string
		contentLength int64
		body          string
		code          DecodeCode
	}{
		{
			name:        "media type",
			contentType: "application/json",
			body:        validDetectionEvent(),
			code:        DecodeInvalidContentType,
		},
		{
			name:          "declared body too large",
			contentType:   "application/cloudevents+json",
			contentLength: MaxBodyBytes + 1,
			body:          `{}`,
			code:          DecodeBodyTooLarge,
		},
		{
			name:        "actual body too large",
			contentType: "application/cloudevents+json",
			body:        oversized,
			code:        DecodeBodyTooLarge,
		},
		{
			name:        "duplicate envelope key",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"id":"evt-20261010-000184",`,
				`"id":"evt-a","id":"evt-b",`,
				1,
			),
			code: DecodeInvalidJSON,
		},
		{
			name:        "duplicate nested key",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"stop_minutes":360`,
				`"stop_minutes":360,"stop_minutes":180`,
				1,
			),
			code: DecodeInvalidJSON,
		},
		{
			name:        "trailing JSON",
			contentType: "application/cloudevents+json",
			body:        validDetectionEvent() + `{}`,
			code:        DecodeInvalidJSON,
		},
		{
			name:        "unknown envelope field",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"specversion":"1.0",`,
				`"specversion":"1.0","traceparent":"secret",`,
				1,
			),
			code: DecodeInvalidCloudEvent,
		},
		{
			name:        "unsupported type",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				string(DelayDetectedType),
				"com.waybill.tracking.damage.detected.v1",
				1,
			),
			code: DecodeUnsupportedType,
		},
		{
			name:        "schema mismatch",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				DelayDetectedSchema,
				DelayCorrectedSchema,
				1,
			),
			code: DecodeUnsupportedSchema,
		},
		{
			name:        "unknown data field",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"waybill_id":"YD2026101001",`,
				`"waybill_id":"YD2026101001","tenant_id":"other",`,
				1,
			),
			code: DecodeInvalidData,
		},
		{
			name:        "fractional version",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"source_version":41`,
				`"source_version":41.5`,
				1,
			),
			code: DecodeInvalidData,
		},
		{
			name:        "wrong subject",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				`"subject":"waybill/YD2026101001"`,
				`"subject":"waybill/YD2026101002"`,
				1,
			),
			code: DecodeInvalidData,
		},
		{
			name:        "future record time",
			contentType: "application/cloudevents+json",
			body: strings.ReplaceAll(
				validDetectionEvent(),
				"2026-10-10T12:30:00Z",
				"2026-10-10T13:06:00Z",
			),
			code: DecodeInvalidData,
		},
		{
			name:        "event after record time",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validDetectionEvent(),
				"2026-10-10T12:28:31Z",
				"2026-10-10T12:31:00Z",
				1,
			),
			code: DecodeInvalidData,
		},
		{
			name:        "cross-source correction",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validCorrectionEvent(),
				`"corrects":{"source":"urn:tms:region-east"`,
				`"corrects":{"source":"urn:tms:region-west"`,
				1,
			),
			code: DecodeInvalidData,
		},
		{
			name:        "retract with replacement",
			contentType: "application/cloudevents+json",
			body: strings.Replace(
				validCorrectionEvent(),
				`"operation":"replace"`,
				`"operation":"retract"`,
				1,
			),
			code: DecodeInvalidData,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeStructured(DecodeRequest{
				ContentType:   test.contentType,
				ContentLength: test.contentLength,
				Body:          strings.NewReader(test.body),
				Now:           decodeNow,
			})
			var decodeErr *DecodeError
			if !errors.As(err, &decodeErr) {
				t.Fatalf("DecodeStructured error = %v, want DecodeError", err)
			}
			if decodeErr.Code != test.code {
				t.Fatalf("DecodeStructured code = %q, want %q: %v", decodeErr.Code, test.code, err)
			}
		})
	}
}

func TestDecodeStructuredRejectsInvalidUTF8(t *testing.T) {
	raw := append([]byte(validDetectionEvent()), 0xff)
	_, err := DecodeStructured(DecodeRequest{
		ContentType: "application/cloudevents+json",
		Body:        bytes.NewReader(raw),
		Now:         decodeNow,
	})
	if code, ok := DecodeErrorCode(err); !ok || code != DecodeInvalidJSON {
		t.Fatalf("DecodeStructured error = %v, want invalid_json", err)
	}
}

func TestEventHashCoversEnvelopeAndData(t *testing.T) {
	original := validDetectionEvent()
	originalHash := decodeEvent(t, original).EventHash()
	changes := []string{
		strings.Replace(original, `"id":"evt-20261010-000184"`, `"id":"evt-20261010-000185"`, 1),
		strings.Replace(original, `"source_version":41`, `"source_version":42`, 1),
		strings.Replace(original, `"stop_minutes":360`, `"stop_minutes":180`, 1),
	}
	for _, changed := range changes {
		if got := decodeEvent(t, changed).EventHash(); got == originalHash {
			t.Fatalf("changed event retained hash %s", got)
		}
	}
}

func TestRestoreCanonicalEvent(t *testing.T) {
	submission := decodeEvent(t, validDetectionEvent())
	record, err := RestoreCanonicalEvent(submission.CanonicalEvent())
	if err != nil {
		t.Fatal(err)
	}
	if record.Ref != submission.Record().Ref ||
		record.DataHash != submission.Record().DataHash ||
		record.Detected == nil ||
		record.Detected.StopMinutes != 360 {
		t.Fatalf("restored record = %#v", record)
	}
}

func TestSubmissionAccessorsReturnCopies(t *testing.T) {
	submission := decodeEvent(t, validDetectionEvent())
	canonical := submission.CanonicalEvent()
	canonical[0] = '['
	if submission.CanonicalEvent()[0] != '{' {
		t.Fatal("CanonicalEvent returned mutable storage")
	}

	record := submission.Record()
	record.Detected.StopMinutes = 1
	if submission.Record().Detected.StopMinutes != 360 {
		t.Fatal("Record returned mutable storage")
	}
}

func TestRestoreResultPreservesCommittedResponse(t *testing.T) {
	raw := []byte(
		`{"event_id":"event-1","incident_id":"incident-1","incident_version":3,"disposition":"corrected"}`,
	)
	result, err := RestoreResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := result.ResponseJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, raw) {
		t.Fatalf("ResponseJSON = %s, want %s", response, raw)
	}
	response[0] = '['
	again, err := result.ResponseJSON()
	if err != nil {
		t.Fatal(err)
	}
	if again[0] != '{' {
		t.Fatal("ResponseJSON returned mutable stored bytes")
	}
}

func decodeEvent(t *testing.T, body string) Submission {
	t.Helper()
	submission, err := DecodeStructured(DecodeRequest{
		ContentType: "application/cloudevents+json; charset=utf-8",
		Body:        strings.NewReader(body),
		Now:         decodeNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func validDetectionEvent() string {
	return `{"specversion":"1.0","id":"evt-20261010-000184","source":"urn:tms:region-east","type":"com.waybill.tracking.delay.detected.v1","subject":"waybill/YD2026101001","time":"2026-10-10T12:30:00Z","datacontenttype":"application/json","dataschema":"urn:waybill-guardian:schema:delay-detected:v1","data":{"waybill_id":"YD2026101001","incident_key":"delay-20261010-000184","source_version":41,"event_time":"2026-10-10T12:28:31Z","record_time":"2026-10-10T12:30:00Z","location":{"code":"MY-N-SERVICE","name":"Mianyang North Service Area"},"business_step":"transporting","reason_code":"stop_duration_exceeded","observations":{"stop_minutes":360}}}`
}

func validCorrectionEvent() string {
	return `{"specversion":"1.0","id":"evt-20261010-000185","source":"urn:tms:region-east","type":"com.waybill.tracking.delay.corrected.v1","subject":"waybill/YD2026101001","time":"2026-10-10T12:45:00Z","datacontenttype":"application/json","dataschema":"urn:waybill-guardian:schema:delay-corrected:v1","data":{"waybill_id":"YD2026101001","incident_key":"delay-20261010-000184","source_version":42,"event_time":"2026-10-10T12:28:31Z","record_time":"2026-10-10T12:45:00Z","corrects":{"source":"urn:tms:region-east","id":"evt-20261010-000184"},"operation":"replace","replacement":{"location":{"code":"MY-S-SERVICE","name":"Mianyang South Service Area"},"business_step":"transporting","reason_code":"stop_duration_exceeded","observations":{"stop_minutes":180}},"reason":"GPS point was assigned to the wrong service area"}}`
}
