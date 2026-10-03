package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

const (
	MaxBodyBytes int64 = 1 << 20

	HashProfileV1 = "waybill-event-v1"

	DelayDetectedType  EventType = "com.waybill.tracking.delay.detected.v1"
	DelayCorrectedType EventType = "com.waybill.tracking.delay.corrected.v1"

	DelayDetectedSchema  = "urn:waybill-guardian:schema:delay-detected:v1"
	DelayCorrectedSchema = "urn:waybill-guardian:schema:delay-corrected:v1"
)

type EventType string
type SourceVersion int64
type Digest string

type EventRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

type Episode struct {
	Source      string
	WaybillID   domain.WaybillID
	IncidentKey string
}

type Location struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type DelayFields struct {
	Location     Location `json:"location"`
	BusinessStep string   `json:"business_step"`
	ReasonCode   string   `json:"reason_code"`
	StopMinutes  int      `json:"stop_minutes"`
}

type DelayDetails struct {
	EventTime time.Time `json:"event_time"`
	DelayFields
}

type CorrectionOperation string

const (
	CorrectionReplace CorrectionOperation = "replace"
	CorrectionRetract CorrectionOperation = "retract"
)

type DelayCorrection struct {
	Corrects    EventRef
	Operation   CorrectionOperation
	EventTime   time.Time
	Replacement *DelayFields
	Reason      string
}

type Record struct {
	Ref           EventRef
	Type          EventType
	Subject       string
	DataSchema    string
	EnvelopeTime  time.Time
	WaybillID     domain.WaybillID
	IncidentKey   string
	SourceVersion SourceVersion
	RecordTime    time.Time
	Detected      *DelayDetails
	Correction    *DelayCorrection
	DataHash      Digest
}

func (r Record) Episode() Episode {
	return Episode{
		Source:      r.Ref.Source,
		WaybillID:   r.WaybillID,
		IncidentKey: r.IncidentKey,
	}
}

func (r Record) Clone() Record {
	cloned := r
	if r.Detected != nil {
		value := *r.Detected
		cloned.Detected = &value
	}
	if r.Correction != nil {
		value := *r.Correction
		if r.Correction.Replacement != nil {
			replacement := *r.Correction.Replacement
			value.Replacement = &replacement
		}
		cloned.Correction = &value
	}
	return cloned
}

type Submission struct {
	record        Record
	canonicalData []byte
	eventJSON     []byte
	eventHash     Digest
}

func (s Submission) Source() string {
	return s.record.Ref.Source
}

func (s Submission) ID() string {
	return s.record.Ref.ID
}

func (s Submission) Type() EventType {
	return s.record.Type
}

func (s Submission) WaybillID() domain.WaybillID {
	return s.record.WaybillID
}

func (s Submission) Record() Record {
	return s.record.Clone()
}

func (s Submission) CanonicalData() []byte {
	return append([]byte(nil), s.canonicalData...)
}

func (s Submission) DataHash() Digest {
	return s.record.DataHash
}

func (s Submission) CanonicalEvent() []byte {
	return append([]byte(nil), s.eventJSON...)
}

func (s Submission) EventHash() Digest {
	return s.eventHash
}

type Disposition string

const (
	DispositionApplied           Disposition = "applied"
	DispositionStale             Disposition = "stale"
	DispositionCorrected         Disposition = "corrected"
	DispositionRetracted         Disposition = "retracted"
	DispositionCorrectionPending Disposition = "correction_pending"
	DispositionManualReview      Disposition = "manual_review"
)

type Result struct {
	EventID         string            `json:"event_id"`
	IncidentID      domain.IncidentID `json:"incident_id"`
	IncidentVersion int64             `json:"incident_version"`
	Disposition     Disposition       `json:"disposition"`
	Replayed        bool              `json:"-"`
}

func (r Result) CanonicalJSON() ([]byte, error) {
	type wireResult struct {
		EventID         string            `json:"event_id"`
		IncidentID      domain.IncidentID `json:"incident_id"`
		IncidentVersion int64             `json:"incident_version"`
		Disposition     Disposition       `json:"disposition"`
	}
	return json.Marshal(wireResult{
		EventID:         r.EventID,
		IncidentID:      r.IncidentID,
		IncidentVersion: r.IncidentVersion,
		Disposition:     r.Disposition,
	})
}

type Ingestor interface {
	IngestEvent(context.Context, Submission) (Result, error)
}
