package proposal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

const SchemaVersion = "proposal.v1"

const (
	maxSummaryRunes           = 500
	maxFactorRunes            = 200
	maxAlternativeReasonRunes = 300
	maxUnavailableReasonRunes = 300
	maxAttributions           = 8
	maxEvidenceRefs           = 8
	maxAlternatives           = 8
	maxProposalBytes          = 32 << 10
)

type IssueCode string

const (
	IssueInvalidJSON        IssueCode = "invalid_json"
	IssueInvalidSchema      IssueCode = "invalid_schema"
	IssueInvalidCitation    IssueCode = "invalid_citation"
	IssueUnsupportedImpact  IssueCode = "unsupported_impact"
	IssueInvalidAlternative IssueCode = "invalid_alternative"
)

var ErrReviewRequired = errors.New("proposal requires manual review")

type ValidationError struct {
	Code IssueCode
	Path string
	Err  error
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s at %s: %v", e.Code, e.Path, e.Err)
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

func CodeOf(err error) IssueCode {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Code
	}
	return IssueInvalidSchema
}

type JSONPointer string

type EvidenceRef struct {
	ToolCallID string          `json:"tool_call_id"`
	FieldPath  JSONPointer     `json:"field_path"`
	Quoted     json.RawMessage `json:"quoted_value"`
}

type AttributionDraft struct {
	Factor        string        `json:"factor"`
	ConfidenceBPS int           `json:"confidence_bps"`
	EvidenceRefs  []EvidenceRef `json:"evidence_refs"`
}

type Alternative struct {
	CarrierID string `json:"carrier_id"`
	Reason    string `json:"reason"`
}

type Availability string

const (
	AvailabilityAvailable   Availability = "available"
	AvailabilityUnavailable Availability = "unavailable"
)

type ImpactMetricDraft struct {
	Availability Availability  `json:"availability"`
	Value        *float64      `json:"value,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
}

type ExpectedImpactDraft struct {
	ETASavedMin  ImpactMetricDraft `json:"eta_saved_min"`
	CostDeltaCNY ImpactMetricDraft `json:"cost_delta_cny"`
}

type Draft struct {
	SchemaVersion  string              `json:"schema_version"`
	Summary        string              `json:"summary"`
	ConfidenceBPS  int                 `json:"confidence_bps"`
	Attribution    []AttributionDraft  `json:"attribution"`
	Alternatives   []Alternative       `json:"alternatives"`
	ExpectedImpact ExpectedImpactDraft `json:"expected_impact"`
}

type Citation struct {
	ToolCallID    string          `json:"tool_call_id"`
	FieldPath     JSONPointer     `json:"field_path"`
	Value         json.RawMessage `json:"value"`
	DisplayValue  string          `json:"display_value"`
	SourceEventID string          `json:"source_event_id"`
	SourceSeq     audit.Seq       `json:"source_seq"`
	SourceHash    string          `json:"source_hash"`
}

type Attribution struct {
	Factor        string     `json:"factor"`
	ConfidenceBPS int        `json:"confidence_bps"`
	Evidence      []Citation `json:"evidence"`
}

type ImpactMetric struct {
	Availability Availability `json:"availability"`
	Reason       string       `json:"reason"`
}

type ExpectedImpact struct {
	ETASavedMin  ImpactMetric `json:"eta_saved_min"`
	CostDeltaCNY ImpactMetric `json:"cost_delta_cny"`
}

type Accepted struct {
	SchemaVersion  string         `json:"schema_version"`
	Summary        string         `json:"summary"`
	ConfidenceBPS  int            `json:"confidence_bps"`
	Attribution    []Attribution  `json:"attribution"`
	Alternatives   []Alternative  `json:"alternatives"`
	ExpectedImpact ExpectedImpact `json:"expected_impact"`
	Digest         string         `json:"digest"`
}

type Compiler struct {
	journal audit.Journal
}

func NewCompiler(journal audit.Journal) (*Compiler, error) {
	if journal == nil {
		return nil, fmt.Errorf("audit journal is required")
	}
	return &Compiler{journal: journal}, nil
}

func Decode(raw []byte) (Draft, error) {
	if len(raw) == 0 || len(raw) > maxProposalBytes {
		return Draft{}, validationError(
			IssueInvalidJSON,
			"",
			fmt.Errorf("proposal must contain between 1 and %d bytes", maxProposalBytes),
		)
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Draft{}, validationError(IssueInvalidJSON, "", err)
	}
	var draft Draft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return Draft{}, validationError(IssueInvalidJSON, "", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("JSON contains a trailing value")
		}
		return Draft{}, validationError(IssueInvalidJSON, "", err)
	}
	if err := validateDraft(draft); err != nil {
		return Draft{}, err
	}
	return cloneDraft(draft), nil
}

func (c *Compiler) Compile(
	ctx context.Context,
	runID domain.RunID,
	raw []byte,
) (Accepted, error) {
	if runID == "" {
		return Accepted{}, validationError(IssueInvalidCitation, "", errors.New("run_id is required"))
	}
	draft, err := Decode(raw)
	if err != nil {
		return Accepted{}, err
	}
	events, err := c.journal.Replay(ctx, runID, 0)
	if err != nil {
		return Accepted{}, fmt.Errorf("read proposal evidence: %w", err)
	}
	ledger, err := buildLedger(events)
	if err != nil {
		return Accepted{}, err
	}

	accepted := Accepted{
		SchemaVersion: draft.SchemaVersion,
		Summary:       draft.Summary,
		ConfidenceBPS: draft.ConfidenceBPS,
		Attribution:   make([]Attribution, 0, len(draft.Attribution)),
		Alternatives:  append([]Alternative(nil), draft.Alternatives...),
		ExpectedImpact: ExpectedImpact{
			ETASavedMin: ImpactMetric{
				Availability: draft.ExpectedImpact.ETASavedMin.Availability,
				Reason:       draft.ExpectedImpact.ETASavedMin.Reason,
			},
			CostDeltaCNY: ImpactMetric{
				Availability: draft.ExpectedImpact.CostDeltaCNY.Availability,
				Reason:       draft.ExpectedImpact.CostDeltaCNY.Reason,
			},
		},
	}
	for attributionIndex, item := range draft.Attribution {
		compiled := Attribution{
			Factor:        item.Factor,
			ConfidenceBPS: item.ConfidenceBPS,
			Evidence:      make([]Citation, 0, len(item.EvidenceRefs)),
		}
		for evidenceIndex, ref := range item.EvidenceRefs {
			citation, resolveErr := ledger.resolve(ref)
			if resolveErr != nil {
				path := fmt.Sprintf("/attribution/%d/evidence_refs/%d", attributionIndex, evidenceIndex)
				return Accepted{}, validationError(IssueInvalidCitation, path, resolveErr)
			}
			compiled.Evidence = append(compiled.Evidence, citation)
		}
		accepted.Attribution = append(accepted.Attribution, compiled)
	}
	for index, alternative := range accepted.Alternatives {
		if _, ok := ledger.candidateCarriers[alternative.CarrierID]; !ok {
			return Accepted{}, validationError(
				IssueInvalidAlternative,
				fmt.Sprintf("/alternatives/%d/carrier_id", index),
				fmt.Errorf("carrier %q is not present in audited waybill candidates", alternative.CarrierID),
			)
		}
	}
	accepted.Digest, err = digest(accepted)
	if err != nil {
		return Accepted{}, err
	}
	return accepted, nil
}

func (c *Compiler) Verify(
	ctx context.Context,
	runID domain.RunID,
	accepted Accepted,
) error {
	if runID == "" {
		return validationError(IssueInvalidCitation, "", errors.New("run_id is required"))
	}
	draft := Draft{
		SchemaVersion: accepted.SchemaVersion,
		Summary:       accepted.Summary,
		ConfidenceBPS: accepted.ConfidenceBPS,
		Attribution:   make([]AttributionDraft, 0, len(accepted.Attribution)),
		Alternatives:  append([]Alternative(nil), accepted.Alternatives...),
		ExpectedImpact: ExpectedImpactDraft{
			ETASavedMin: ImpactMetricDraft{
				Availability: accepted.ExpectedImpact.ETASavedMin.Availability,
				Reason:       accepted.ExpectedImpact.ETASavedMin.Reason,
			},
			CostDeltaCNY: ImpactMetricDraft{
				Availability: accepted.ExpectedImpact.CostDeltaCNY.Availability,
				Reason:       accepted.ExpectedImpact.CostDeltaCNY.Reason,
			},
		},
	}
	for _, item := range accepted.Attribution {
		attribution := AttributionDraft{
			Factor:        item.Factor,
			ConfidenceBPS: item.ConfidenceBPS,
			EvidenceRefs:  make([]EvidenceRef, 0, len(item.Evidence)),
		}
		for _, citation := range item.Evidence {
			attribution.EvidenceRefs = append(attribution.EvidenceRefs, EvidenceRef{
				ToolCallID: citation.ToolCallID,
				FieldPath:  citation.FieldPath,
				Quoted:     append(json.RawMessage(nil), citation.Value...),
			})
		}
		draft.Attribution = append(draft.Attribution, attribution)
	}
	if err := validateDraft(draft); err != nil {
		return err
	}

	events, err := c.journal.Replay(ctx, runID, 0)
	if err != nil {
		return fmt.Errorf("read proposal evidence: %w", err)
	}
	ledger, err := buildLedger(events)
	if err != nil {
		return err
	}
	for attributionIndex, item := range accepted.Attribution {
		for evidenceIndex, citation := range item.Evidence {
			expected, resolveErr := ledger.resolve(EvidenceRef{
				ToolCallID: citation.ToolCallID,
				FieldPath:  citation.FieldPath,
				Quoted:     citation.Value,
			})
			if resolveErr != nil {
				path := fmt.Sprintf("/attribution/%d/evidence/%d", attributionIndex, evidenceIndex)
				return validationError(IssueInvalidCitation, path, resolveErr)
			}
			if !sameCitation(citation, expected) {
				path := fmt.Sprintf("/attribution/%d/evidence/%d", attributionIndex, evidenceIndex)
				return validationError(
					IssueInvalidCitation,
					path,
					errors.New("citation source metadata does not match the audit event"),
				)
			}
		}
	}
	for index, alternative := range accepted.Alternatives {
		if _, ok := ledger.candidateCarriers[alternative.CarrierID]; !ok {
			return validationError(
				IssueInvalidAlternative,
				fmt.Sprintf("/alternatives/%d/carrier_id", index),
				fmt.Errorf("carrier %q is not present in audited waybill candidates", alternative.CarrierID),
			)
		}
	}
	expectedDigest, err := digest(accepted)
	if err != nil {
		return err
	}
	if accepted.Digest == "" || accepted.Digest != expectedDigest {
		return validationError(
			IssueInvalidSchema,
			"/digest",
			errors.New("does not match the accepted proposal"),
		)
	}
	return nil
}

func sameCitation(actual, expected Citation) bool {
	return actual.ToolCallID == expected.ToolCallID &&
		actual.FieldPath == expected.FieldPath &&
		bytes.Equal(actual.Value, expected.Value) &&
		actual.DisplayValue == expected.DisplayValue &&
		actual.SourceEventID == expected.SourceEventID &&
		actual.SourceSeq == expected.SourceSeq &&
		actual.SourceHash == expected.SourceHash
}

func validateDraft(draft Draft) error {
	if draft.SchemaVersion != SchemaVersion {
		return validationError(
			IssueInvalidSchema,
			"/schema_version",
			fmt.Errorf("must equal %q", SchemaVersion),
		)
	}
	if err := validateText(draft.Summary, maxSummaryRunes); err != nil {
		return validationError(IssueInvalidSchema, "/summary", err)
	}
	if err := validateConfidence(draft.ConfidenceBPS); err != nil {
		return validationError(IssueInvalidSchema, "/confidence_bps", err)
	}
	if len(draft.Attribution) == 0 || len(draft.Attribution) > maxAttributions {
		return validationError(
			IssueInvalidSchema,
			"/attribution",
			fmt.Errorf("must contain between 1 and %d items", maxAttributions),
		)
	}
	for index, item := range draft.Attribution {
		path := fmt.Sprintf("/attribution/%d", index)
		if err := validateText(item.Factor, maxFactorRunes); err != nil {
			return validationError(IssueInvalidSchema, path+"/factor", err)
		}
		if err := validateConfidence(item.ConfidenceBPS); err != nil {
			return validationError(IssueInvalidSchema, path+"/confidence_bps", err)
		}
		if len(item.EvidenceRefs) == 0 || len(item.EvidenceRefs) > maxEvidenceRefs {
			return validationError(
				IssueInvalidSchema,
				path+"/evidence_refs",
				fmt.Errorf("must contain between 1 and %d items", maxEvidenceRefs),
			)
		}
		for evidenceIndex, ref := range item.EvidenceRefs {
			refPath := fmt.Sprintf("%s/evidence_refs/%d", path, evidenceIndex)
			if strings.TrimSpace(ref.ToolCallID) == "" || ref.ToolCallID != strings.TrimSpace(ref.ToolCallID) {
				return validationError(
					IssueInvalidSchema,
					refPath+"/tool_call_id",
					errors.New("must be non-empty without surrounding whitespace"),
				)
			}
			if _, err := parsePointer(ref.FieldPath); err != nil {
				return validationError(IssueInvalidSchema, refPath+"/field_path", err)
			}
			if _, _, err := decodeScalar(ref.Quoted); err != nil {
				return validationError(IssueInvalidSchema, refPath+"/quoted_value", err)
			}
		}
	}
	if len(draft.Alternatives) > maxAlternatives {
		return validationError(
			IssueInvalidSchema,
			"/alternatives",
			fmt.Errorf("must contain at most %d items", maxAlternatives),
		)
	}
	seenCarriers := make(map[string]struct{}, len(draft.Alternatives))
	for index, item := range draft.Alternatives {
		path := fmt.Sprintf("/alternatives/%d", index)
		if err := validateText(item.CarrierID, maxFactorRunes); err != nil {
			return validationError(IssueInvalidSchema, path+"/carrier_id", err)
		}
		if _, exists := seenCarriers[item.CarrierID]; exists {
			return validationError(
				IssueInvalidSchema,
				path+"/carrier_id",
				fmt.Errorf("duplicate carrier %q", item.CarrierID),
			)
		}
		seenCarriers[item.CarrierID] = struct{}{}
		if err := validateText(item.Reason, maxAlternativeReasonRunes); err != nil {
			return validationError(IssueInvalidSchema, path+"/reason", err)
		}
	}
	if err := validateImpactMetric("/expected_impact/eta_saved_min", draft.ExpectedImpact.ETASavedMin); err != nil {
		return err
	}
	if err := validateImpactMetric("/expected_impact/cost_delta_cny", draft.ExpectedImpact.CostDeltaCNY); err != nil {
		return err
	}
	return nil
}

func validateImpactMetric(path string, metric ImpactMetricDraft) error {
	switch metric.Availability {
	case AvailabilityUnavailable:
		if metric.Value != nil || len(metric.EvidenceRefs) != 0 {
			return validationError(
				IssueInvalidSchema,
				path,
				errors.New("unavailable metrics must not contain value or evidence_refs"),
			)
		}
		if err := validateText(metric.Reason, maxUnavailableReasonRunes); err != nil {
			return validationError(IssueInvalidSchema, path+"/reason", err)
		}
		return nil
	case AvailabilityAvailable:
		return validationError(
			IssueUnsupportedImpact,
			path,
			errors.New("current tool contracts do not provide authoritative impact fields"),
		)
	default:
		return validationError(
			IssueInvalidSchema,
			path+"/availability",
			errors.New("must be available or unavailable"),
		)
	}
}

func validateText(value string, maxRunes int) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("must be non-empty without surrounding whitespace")
	}
	if !utf8.ValidString(value) {
		return errors.New("must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("must contain at most %d characters", maxRunes)
	}
	return nil
}

func validateConfidence(value int) error {
	if value < 1 || value > 10000 {
		return errors.New("must be between 1 and 10000")
	}
	return nil
}

type evidenceEntry struct {
	action domain.Action
	event  audit.Event
	result any
}

type evidenceLedger struct {
	entries           map[string]evidenceEntry
	duplicates        map[string]struct{}
	candidateCarriers map[string]struct{}
}

func buildLedger(events []audit.Event) (evidenceLedger, error) {
	ledger := evidenceLedger{
		entries:           make(map[string]evidenceEntry),
		duplicates:        make(map[string]struct{}),
		candidateCarriers: make(map[string]struct{}),
	}
	for _, event := range events {
		if event.Type != audit.EventToolResult {
			continue
		}
		var payload struct {
			CallID string          `json:"call_id"`
			Action domain.Action   `json:"action"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.CallID == "" {
			continue
		}
		if _, exists := ledger.entries[payload.CallID]; exists {
			ledger.duplicates[payload.CallID] = struct{}{}
			continue
		}
		entry := evidenceEntry{action: payload.Action, event: event}
		if payload.Error == "" && isReadAction(payload.Action) && len(payload.Result) > 0 {
			result, err := decodeJSONValue(payload.Result)
			if err != nil {
				return evidenceLedger{}, validationError(
					IssueInvalidCitation,
					"",
					fmt.Errorf("decode audited result for call %q: %w", payload.CallID, err),
				)
			}
			entry.result = result
			if payload.Action == domain.ActionGetWaybill {
				collectCandidateCarriers(result, ledger.candidateCarriers)
			}
		}
		ledger.entries[payload.CallID] = entry
	}
	return ledger, nil
}

func (l evidenceLedger) resolve(ref EvidenceRef) (Citation, error) {
	if _, duplicate := l.duplicates[ref.ToolCallID]; duplicate {
		return Citation{}, fmt.Errorf("tool call %q has duplicate result events", ref.ToolCallID)
	}
	entry, ok := l.entries[ref.ToolCallID]
	if !ok {
		return Citation{}, fmt.Errorf("tool call %q has no result in this run", ref.ToolCallID)
	}
	if !isReadAction(entry.action) {
		return Citation{}, fmt.Errorf("tool call %q is not a read tool", ref.ToolCallID)
	}
	if entry.result == nil {
		return Citation{}, fmt.Errorf("tool call %q did not produce a successful result", ref.ToolCallID)
	}
	value, err := resolvePointer(entry.result, ref.FieldPath)
	if err != nil {
		return Citation{}, err
	}
	actual, display, err := encodeScalar(value)
	if err != nil {
		return Citation{}, fmt.Errorf("field %q: %w", ref.FieldPath, err)
	}
	quoted, _, err := decodeScalar(ref.Quoted)
	if err != nil {
		return Citation{}, err
	}
	if !bytes.Equal(actual, quoted) {
		return Citation{}, fmt.Errorf(
			"quoted value %s does not match audited value %s",
			quoted,
			actual,
		)
	}
	return Citation{
		ToolCallID:    ref.ToolCallID,
		FieldPath:     ref.FieldPath,
		Value:         append(json.RawMessage(nil), actual...),
		DisplayValue:  display,
		SourceEventID: entry.event.EventID,
		SourceSeq:     entry.event.Seq,
		SourceHash:    entry.event.Hash,
	}, nil
}

func isReadAction(action domain.Action) bool {
	switch action {
	case domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather:
		return true
	default:
		return false
	}
}

func collectCandidateCarriers(value any, destination map[string]struct{}) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	candidates, ok := object["candidate_carriers"].([]any)
	if !ok {
		return
	}
	for _, candidate := range candidates {
		fields, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		carrierID, ok := fields["carrier_id"].(string)
		if ok && carrierID != "" {
			destination[carrierID] = struct{}{}
		}
	}
}

func parsePointer(pointer JSONPointer) ([]string, error) {
	raw := string(pointer)
	if raw == "" {
		return nil, nil
	}
	if !strings.HasPrefix(raw, "/") {
		return nil, errors.New("must be an RFC 6901 JSON Pointer")
	}
	encoded := strings.Split(raw[1:], "/")
	tokens := make([]string, 0, len(encoded))
	for _, item := range encoded {
		var decoded strings.Builder
		for index := 0; index < len(item); index++ {
			if item[index] != '~' {
				decoded.WriteByte(item[index])
				continue
			}
			if index+1 >= len(item) {
				return nil, errors.New("contains an incomplete RFC 6901 escape")
			}
			index++
			switch item[index] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, fmt.Errorf("contains invalid RFC 6901 escape ~%c", item[index])
			}
		}
		tokens = append(tokens, decoded.String())
	}
	return tokens, nil
}

func resolvePointer(document any, pointer JSONPointer) (any, error) {
	tokens, err := parsePointer(pointer)
	if err != nil {
		return nil, err
	}
	current := document
	for _, token := range tokens {
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[token]
			if !ok {
				return nil, fmt.Errorf("field path %q does not exist", pointer)
			}
			current = next
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("field path %q contains an invalid array index", pointer)
			}
			index, convertErr := strconv.Atoi(token)
			if convertErr != nil || index < 0 || index >= len(value) {
				return nil, fmt.Errorf("field path %q contains an invalid array index", pointer)
			}
			current = value[index]
		default:
			return nil, fmt.Errorf("field path %q traverses a scalar value", pointer)
		}
	}
	return current, nil
}

func decodeScalar(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return nil, "", errors.New("must contain a quoted scalar")
	}
	value, err := decodeJSONValue(raw)
	if err != nil {
		return nil, "", err
	}
	return encodeScalar(value)
}

func encodeScalar(value any) (json.RawMessage, string, error) {
	switch typed := value.(type) {
	case nil:
		return json.RawMessage("null"), "null", nil
	case string:
		raw, err := json.Marshal(typed)
		return raw, typed, err
	case bool:
		raw, err := json.Marshal(typed)
		return raw, strconv.FormatBool(typed), err
	case json.Number:
		raw, err := json.Marshal(typed)
		return raw, typed.String(), err
	default:
		return nil, "", errors.New("must resolve to a JSON scalar")
	}
}

func decodeJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("JSON contains a trailing value")
		}
		return nil, err
	}
	return value, nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON contains a trailing token %v", token)
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
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
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON object contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func digest(accepted Accepted) (string, error) {
	accepted.Digest = ""
	raw, err := json.Marshal(accepted)
	if err != nil {
		return "", fmt.Errorf("marshal accepted proposal: %w", err)
	}
	sum := sha256.Sum256(append([]byte("waybill-proposal-v1\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func cloneDraft(draft Draft) Draft {
	cloned := draft
	cloned.Attribution = append([]AttributionDraft(nil), draft.Attribution...)
	for index := range cloned.Attribution {
		cloned.Attribution[index].EvidenceRefs = append(
			[]EvidenceRef(nil),
			draft.Attribution[index].EvidenceRefs...,
		)
		for refIndex := range cloned.Attribution[index].EvidenceRefs {
			cloned.Attribution[index].EvidenceRefs[refIndex].Quoted = append(
				json.RawMessage(nil),
				draft.Attribution[index].EvidenceRefs[refIndex].Quoted...,
			)
		}
	}
	cloned.Alternatives = append([]Alternative(nil), draft.Alternatives...)
	cloned.ExpectedImpact.ETASavedMin.EvidenceRefs = cloneEvidenceRefs(
		draft.ExpectedImpact.ETASavedMin.EvidenceRefs,
	)
	cloned.ExpectedImpact.CostDeltaCNY.EvidenceRefs = cloneEvidenceRefs(
		draft.ExpectedImpact.CostDeltaCNY.EvidenceRefs,
	)
	return cloned
}

func cloneEvidenceRefs(values []EvidenceRef) []EvidenceRef {
	cloned := append([]EvidenceRef(nil), values...)
	for index := range cloned {
		cloned[index].Quoted = append(json.RawMessage(nil), values[index].Quoted...)
	}
	return cloned
}

func validationError(code IssueCode, path string, err error) error {
	return &ValidationError{Code: code, Path: path, Err: err}
}
