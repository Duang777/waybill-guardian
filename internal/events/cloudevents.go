package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

const futureRecordTimeTolerance = 5 * time.Minute

var (
	eventIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	incidentKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)
)

type DecodeRequest struct {
	ContentType   string
	ContentLength int64
	Body          io.Reader
	Now           time.Time
}

type envelopeWire struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

type locationWire struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type observationsWire struct {
	StopMinutes int `json:"stop_minutes"`
}

type delayFieldsWire struct {
	Location     locationWire     `json:"location"`
	BusinessStep string           `json:"business_step"`
	ReasonCode   string           `json:"reason_code"`
	Observations observationsWire `json:"observations"`
}

type delayDetectedWire struct {
	WaybillID     string `json:"waybill_id"`
	IncidentKey   string `json:"incident_key"`
	SourceVersion int64  `json:"source_version"`
	EventTime     string `json:"event_time"`
	RecordTime    string `json:"record_time"`
	delayFieldsWire
}

type correctionRefWire struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

type delayCorrectionWire struct {
	WaybillID     string            `json:"waybill_id"`
	IncidentKey   string            `json:"incident_key"`
	SourceVersion int64             `json:"source_version"`
	EventTime     string            `json:"event_time"`
	RecordTime    string            `json:"record_time"`
	Corrects      correctionRefWire `json:"corrects"`
	Operation     string            `json:"operation"`
	Replacement   *delayFieldsWire  `json:"replacement,omitempty"`
	Reason        string            `json:"reason"`
}

type canonicalEnvelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            EventType       `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func DecodeStructured(request DecodeRequest) (Submission, error) {
	if err := validateContentType(request.ContentType); err != nil {
		return Submission{}, err
	}
	if request.ContentLength > MaxBodyBytes {
		return Submission{}, decodeError(
			DecodeBodyTooLarge,
			"request body exceeds %d bytes",
			MaxBodyBytes,
		)
	}
	if request.Body == nil {
		return Submission{}, decodeError(DecodeInvalidJSON, "request body is required")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil {
		return Submission{}, decodeError(DecodeInvalidJSON, "read request body: %v", err)
	}
	if int64(len(raw)) > MaxBodyBytes {
		return Submission{}, decodeError(
			DecodeBodyTooLarge,
			"request body exceeds %d bytes",
			MaxBodyBytes,
		)
	}
	if !utf8.Valid(raw) {
		return Submission{}, decodeError(DecodeInvalidJSON, "request body is not valid UTF-8")
	}
	if err := validateJSONObject(raw); err != nil {
		return Submission{}, decodeError(DecodeInvalidJSON, "%v", err)
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	return decodeCanonicalEvent(raw, request.Now.UTC(), true)
}

func RestoreCanonicalEvent(raw []byte) (Record, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return Record{}, fmt.Errorf("canonical event is empty or invalid UTF-8")
	}
	if err := validateJSONObject(raw); err != nil {
		return Record{}, fmt.Errorf("validate canonical event: %w", err)
	}
	submission, err := decodeCanonicalEvent(raw, time.Time{}, false)
	if err != nil {
		return Record{}, fmt.Errorf("restore canonical event: %w", err)
	}
	return submission.Record(), nil
}

func decodeCanonicalEvent(raw []byte, now time.Time, checkFuture bool) (Submission, error) {
	var envelope envelopeWire
	if err := strictUnmarshal(raw, &envelope); err != nil {
		return Submission{}, decodeError(DecodeInvalidCloudEvent, "%v", err)
	}
	eventType, envelopeTime, err := validateEnvelope(envelope)
	if err != nil {
		return Submission{}, err
	}

	var record Record
	var canonicalDataBytes []byte
	switch eventType {
	case DelayDetectedType:
		if envelope.DataSchema != DelayDetectedSchema {
			return Submission{}, decodeError(
				DecodeUnsupportedSchema,
				"dataschema does not match event type",
			)
		}
		record, canonicalDataBytes, err = decodeDelayDetected(envelope, envelopeTime, now, checkFuture)
	case DelayCorrectedType:
		if envelope.DataSchema != DelayCorrectedSchema {
			return Submission{}, decodeError(
				DecodeUnsupportedSchema,
				"dataschema does not match event type",
			)
		}
		record, canonicalDataBytes, err = decodeDelayCorrection(envelope, envelopeTime, now, checkFuture)
	default:
		return Submission{}, decodeError(DecodeUnsupportedType, "unsupported event type")
	}
	if err != nil {
		return Submission{}, err
	}

	eventJSON, err := json.Marshal(canonicalEnvelope{
		SpecVersion:     envelope.SpecVersion,
		ID:              envelope.ID,
		Source:          envelope.Source,
		Type:            eventType,
		Subject:         envelope.Subject,
		Time:            formatTime(envelopeTime),
		DataContentType: envelope.DataContentType,
		DataSchema:      envelope.DataSchema,
		Data:            canonicalDataBytes,
	})
	if err != nil {
		return Submission{}, decodeError(DecodeInvalidCloudEvent, "canonicalize event: %v", err)
	}
	record.DataHash = digest("waybill-event-data-v1", canonicalDataBytes)
	return Submission{
		record:        record,
		canonicalData: canonicalDataBytes,
		eventJSON:     eventJSON,
		eventHash:     digest(HashProfileV1, eventJSON),
	}, nil
}

func validateContentType(value string) error {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/cloudevents+json" {
		return decodeError(
			DecodeInvalidContentType,
			"Content-Type must be application/cloudevents+json",
		)
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return decodeError(
				DecodeInvalidContentType,
				"unsupported Content-Type parameter",
			)
		}
	}
	return nil
}

func validateEnvelope(envelope envelopeWire) (EventType, time.Time, error) {
	if envelope.SpecVersion != "1.0" {
		return "", time.Time{}, decodeError(
			DecodeInvalidCloudEvent,
			"specversion must be 1.0",
		)
	}
	if !eventIDPattern.MatchString(envelope.ID) {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "invalid event id")
	}
	if len(envelope.Source) == 0 || len(envelope.Source) > 256 {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "invalid event source")
	}
	source, err := url.Parse(envelope.Source)
	if err != nil || !source.IsAbs() || source.Fragment != "" {
		return "", time.Time{}, decodeError(
			DecodeInvalidCloudEvent,
			"source must be an absolute URI without a fragment",
		)
	}
	eventType := EventType(envelope.Type)
	switch eventType {
	case DelayDetectedType, DelayCorrectedType:
	default:
		return "", time.Time{}, decodeError(DecodeUnsupportedType, "unsupported event type")
	}
	if envelope.Subject == "" {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "subject is required")
	}
	envelopeTime, err := parseTime(envelope.Time, "time")
	if err != nil {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "%v", err)
	}
	if envelope.DataContentType != "application/json" {
		return "", time.Time{}, decodeError(
			DecodeInvalidCloudEvent,
			"datacontenttype must be application/json",
		)
	}
	if envelope.DataSchema == "" {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "dataschema is required")
	}
	if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return "", time.Time{}, decodeError(DecodeInvalidCloudEvent, "data must be an object")
	}
	return eventType, envelopeTime, nil
}

func decodeDelayDetected(
	envelope envelopeWire,
	envelopeTime time.Time,
	now time.Time,
	checkFuture bool,
) (Record, []byte, error) {
	var data delayDetectedWire
	if err := strictUnmarshal(envelope.Data, &data); err != nil {
		return Record{}, nil, decodeError(DecodeInvalidData, "%v", err)
	}
	common, err := validateCommonData(
		data.WaybillID,
		data.IncidentKey,
		data.SourceVersion,
		data.EventTime,
		data.RecordTime,
		envelope.Subject,
		envelopeTime,
		now,
		checkFuture,
	)
	if err != nil {
		return Record{}, nil, err
	}
	fields, err := validateDelayFields(data.delayFieldsWire)
	if err != nil {
		return Record{}, nil, err
	}
	canonicalDataBytes, err := json.Marshal(struct {
		WaybillID     domain.WaybillID `json:"waybill_id"`
		IncidentKey   string           `json:"incident_key"`
		SourceVersion SourceVersion    `json:"source_version"`
		EventTime     string           `json:"event_time"`
		RecordTime    string           `json:"record_time"`
		Location      Location         `json:"location"`
		BusinessStep  string           `json:"business_step"`
		ReasonCode    string           `json:"reason_code"`
		Observations  observationsWire `json:"observations"`
	}{
		WaybillID:     common.waybillID,
		IncidentKey:   data.IncidentKey,
		SourceVersion: SourceVersion(data.SourceVersion),
		EventTime:     formatTime(common.eventTime),
		RecordTime:    formatTime(common.recordTime),
		Location:      fields.Location,
		BusinessStep:  fields.BusinessStep,
		ReasonCode:    fields.ReasonCode,
		Observations:  observationsWire{StopMinutes: fields.StopMinutes},
	})
	if err != nil {
		return Record{}, nil, decodeError(DecodeInvalidData, "canonicalize data: %v", err)
	}
	return Record{
		Ref:           EventRef{Source: envelope.Source, ID: envelope.ID},
		Type:          DelayDetectedType,
		Subject:       envelope.Subject,
		DataSchema:    envelope.DataSchema,
		EnvelopeTime:  envelopeTime,
		WaybillID:     common.waybillID,
		IncidentKey:   data.IncidentKey,
		SourceVersion: SourceVersion(data.SourceVersion),
		RecordTime:    common.recordTime,
		Detected: &DelayDetails{
			EventTime:   common.eventTime,
			DelayFields: fields,
		},
	}, canonicalDataBytes, nil
}

func decodeDelayCorrection(
	envelope envelopeWire,
	envelopeTime time.Time,
	now time.Time,
	checkFuture bool,
) (Record, []byte, error) {
	var data delayCorrectionWire
	if err := strictUnmarshal(envelope.Data, &data); err != nil {
		return Record{}, nil, decodeError(DecodeInvalidData, "%v", err)
	}
	common, err := validateCommonData(
		data.WaybillID,
		data.IncidentKey,
		data.SourceVersion,
		data.EventTime,
		data.RecordTime,
		envelope.Subject,
		envelopeTime,
		now,
		checkFuture,
	)
	if err != nil {
		return Record{}, nil, err
	}
	if data.Corrects.Source != envelope.Source ||
		!eventIDPattern.MatchString(data.Corrects.ID) ||
		data.Corrects.ID == envelope.ID {
		return Record{}, nil, decodeError(
			DecodeInvalidData,
			"correction target must be a different event from the same source",
		)
	}
	operation := CorrectionOperation(data.Operation)
	var replacement *DelayFields
	switch operation {
	case CorrectionReplace:
		if data.Replacement == nil {
			return Record{}, nil, decodeError(
				DecodeInvalidData,
				"replace correction requires replacement",
			)
		}
		value, err := validateDelayFields(*data.Replacement)
		if err != nil {
			return Record{}, nil, err
		}
		replacement = &value
	case CorrectionRetract:
		if data.Replacement != nil {
			return Record{}, nil, decodeError(
				DecodeInvalidData,
				"retract correction forbids replacement",
			)
		}
	default:
		return Record{}, nil, decodeError(
			DecodeInvalidData,
			"operation must be replace or retract",
		)
	}
	if strings.TrimSpace(data.Reason) == "" || len(data.Reason) > 512 {
		return Record{}, nil, decodeError(
			DecodeInvalidData,
			"reason must contain 1 to 512 UTF-8 bytes",
		)
	}

	type canonicalReplacement struct {
		Location     Location         `json:"location"`
		BusinessStep string           `json:"business_step"`
		ReasonCode   string           `json:"reason_code"`
		Observations observationsWire `json:"observations"`
	}
	var canonicalReplacementValue *canonicalReplacement
	if replacement != nil {
		canonicalReplacementValue = &canonicalReplacement{
			Location:     replacement.Location,
			BusinessStep: replacement.BusinessStep,
			ReasonCode:   replacement.ReasonCode,
			Observations: observationsWire{StopMinutes: replacement.StopMinutes},
		}
	}
	canonicalDataBytes, err := json.Marshal(struct {
		WaybillID     domain.WaybillID      `json:"waybill_id"`
		IncidentKey   string                `json:"incident_key"`
		SourceVersion SourceVersion         `json:"source_version"`
		EventTime     string                `json:"event_time"`
		RecordTime    string                `json:"record_time"`
		Corrects      EventRef              `json:"corrects"`
		Operation     CorrectionOperation   `json:"operation"`
		Replacement   *canonicalReplacement `json:"replacement,omitempty"`
		Reason        string                `json:"reason"`
	}{
		WaybillID:     common.waybillID,
		IncidentKey:   data.IncidentKey,
		SourceVersion: SourceVersion(data.SourceVersion),
		EventTime:     formatTime(common.eventTime),
		RecordTime:    formatTime(common.recordTime),
		Corrects:      EventRef{Source: data.Corrects.Source, ID: data.Corrects.ID},
		Operation:     operation,
		Replacement:   canonicalReplacementValue,
		Reason:        data.Reason,
	})
	if err != nil {
		return Record{}, nil, decodeError(DecodeInvalidData, "canonicalize data: %v", err)
	}
	return Record{
		Ref:           EventRef{Source: envelope.Source, ID: envelope.ID},
		Type:          DelayCorrectedType,
		Subject:       envelope.Subject,
		DataSchema:    envelope.DataSchema,
		EnvelopeTime:  envelopeTime,
		WaybillID:     common.waybillID,
		IncidentKey:   data.IncidentKey,
		SourceVersion: SourceVersion(data.SourceVersion),
		RecordTime:    common.recordTime,
		Correction: &DelayCorrection{
			Corrects:    EventRef{Source: data.Corrects.Source, ID: data.Corrects.ID},
			Operation:   operation,
			EventTime:   common.eventTime,
			Replacement: replacement,
			Reason:      data.Reason,
		},
	}, canonicalDataBytes, nil
}

type commonData struct {
	waybillID  domain.WaybillID
	eventTime  time.Time
	recordTime time.Time
}

func validateCommonData(
	waybillValue string,
	incidentKey string,
	sourceVersion int64,
	eventTimeValue string,
	recordTimeValue string,
	subject string,
	envelopeTime time.Time,
	now time.Time,
	checkFuture bool,
) (commonData, error) {
	waybillID := domain.WaybillID(waybillValue)
	if err := domain.ValidateWaybillID(waybillID); err != nil {
		return commonData{}, decodeError(DecodeInvalidData, "invalid waybill_id")
	}
	if subject != "waybill/"+waybillValue {
		return commonData{}, decodeError(
			DecodeInvalidData,
			"subject does not match waybill_id",
		)
	}
	if !incidentKeyPattern.MatchString(incidentKey) {
		return commonData{}, decodeError(DecodeInvalidData, "invalid incident_key")
	}
	if sourceVersion <= 0 {
		return commonData{}, decodeError(
			DecodeInvalidData,
			"source_version must be a positive integer",
		)
	}
	eventTime, err := parseTime(eventTimeValue, "event_time")
	if err != nil {
		return commonData{}, decodeError(DecodeInvalidData, "%v", err)
	}
	recordTime, err := parseTime(recordTimeValue, "record_time")
	if err != nil {
		return commonData{}, decodeError(DecodeInvalidData, "%v", err)
	}
	if eventTime.After(recordTime) {
		return commonData{}, decodeError(
			DecodeInvalidData,
			"event_time cannot be after record_time",
		)
	}
	if !envelopeTime.Equal(recordTime) {
		return commonData{}, decodeError(
			DecodeInvalidData,
			"CloudEvent time must equal record_time",
		)
	}
	if checkFuture && recordTime.After(now.Add(futureRecordTimeTolerance)) {
		return commonData{}, decodeError(
			DecodeInvalidData,
			"record_time is too far in the future",
		)
	}
	return commonData{
		waybillID:  waybillID,
		eventTime:  eventTime,
		recordTime: recordTime,
	}, nil
}

func validateDelayFields(value delayFieldsWire) (DelayFields, error) {
	if !printableASCII(value.Location.Code, 1, 64) {
		return DelayFields{}, decodeError(DecodeInvalidData, "invalid location.code")
	}
	if value.Location.Name == "" || len(value.Location.Name) > 128 {
		return DelayFields{}, decodeError(DecodeInvalidData, "invalid location.name")
	}
	if value.BusinessStep != "transporting" {
		return DelayFields{}, decodeError(
			DecodeInvalidData,
			"business_step must be transporting",
		)
	}
	if value.ReasonCode != "stop_duration_exceeded" {
		return DelayFields{}, decodeError(
			DecodeInvalidData,
			"reason_code must be stop_duration_exceeded",
		)
	}
	if value.Observations.StopMinutes < 1 || value.Observations.StopMinutes > 10080 {
		return DelayFields{}, decodeError(
			DecodeInvalidData,
			"stop_minutes must be between 1 and 10080",
		)
	}
	return DelayFields{
		Location: Location{
			Code: value.Location.Code,
			Name: value.Location.Name,
		},
		BusinessStep: value.BusinessStep,
		ReasonCode:   value.ReasonCode,
		StopMinutes:  value.Observations.StopMinutes,
	}, nil
}

func parseTime(value, field string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339Nano", field)
	}
	return parsed.UTC(), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func printableASCII(value string, minLength, maxLength int) bool {
	if len(value) < minLength || len(value) > maxLength {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func strictUnmarshal(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON contains a trailing value")
		}
		return err
	}
	return nil
}

func validateJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("top-level JSON value must be an object")
	}
	if err := scanObject(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON contains a trailing value")
		}
		return err
	}
	return nil
}

func scanObject(decoder *json.Decoder) error {
	keys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("JSON object key must be a string")
		}
		if _, exists := keys[key]; exists {
			return fmt.Errorf("duplicate JSON key %q", key)
		}
		keys[key] = struct{}{}
		if err := scanValue(decoder); err != nil {
			return err
		}
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('}') {
		return fmt.Errorf("JSON object is not closed")
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return scanObject(decoder)
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token != json.Delim(']') {
			return fmt.Errorf("JSON array is not closed")
		}
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func digest(profile string, value []byte) Digest {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(profile))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(value)
	return Digest(hex.EncodeToString(hasher.Sum(nil)))
}
