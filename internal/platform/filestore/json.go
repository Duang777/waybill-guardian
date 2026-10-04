package filestore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

func parseJSON(raw []byte) (datasetDraft, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return datasetDraft{}, fmt.Errorf("json: data is empty")
	}
	if !utf8.Valid(raw) {
		return datasetDraft{}, fmt.Errorf("json: data is not valid UTF-8")
	}
	if err := rejectDuplicateFields(raw); err != nil {
		return datasetDraft{}, err
	}
	if err := rejectMissingRequiredFields(raw); err != nil {
		return datasetDraft{}, err
	}

	var draft datasetDraft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return datasetDraft{}, fmt.Errorf("json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return datasetDraft{}, fmt.Errorf("json: trailing value is not allowed")
		}
		return datasetDraft{}, fmt.Errorf("json: trailing value: %w", err)
	}
	assignJSONLocations(&draft)
	return draft, nil
}

func rejectDuplicateFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("json: top-level value must be an object")
	}
	return rejectDuplicateObjectFields(decoder, "json")
}

func rejectDuplicateObjectFields(decoder *json.Decoder, location string) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("json: %w", err)
		}
		field, ok := token.(string)
		if !ok {
			return fmt.Errorf("json: object field name is invalid")
		}
		if _, exists := seen[field]; exists {
			return &ValidationError{Issues: []ValidationIssue{{
				Location: location,
				Field:    field,
				Code:     "duplicate_field",
				Message:  duplicateFieldMessage(location),
			}}}
		}
		seen[field] = struct{}{}
		if err := rejectDuplicateValueFields(
			decoder,
			location+"."+field,
		); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
}

func rejectDuplicateValueFields(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return rejectDuplicateObjectFields(decoder, location)
	case '[':
		index := 0
		for decoder.More() {
			if err := rejectDuplicateValueFields(
				decoder,
				fmt.Sprintf("%s[%d]", location, index),
			); err != nil {
				return err
			}
			index++
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("json: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("json: unexpected delimiter %q", delimiter)
	}
}

func duplicateFieldMessage(location string) string {
	if location == "json" {
		return "duplicate top-level field"
	}
	return "duplicate field in " + location
}

var requiredJSONFields = map[string][]string{
	"waybills": {
		"waybill_id",
		"origin",
		"destination",
		"cargo",
		"current_carrier_id",
		"driver_id",
		"status",
		"sla_hours",
		"shipper_phone",
	},
	"drivers": {
		"driver_id",
		"name",
		"phone",
		"plate",
		"continuous_drive_hours",
		"fatigue_alert",
	},
	"waybill_candidates": {
		"waybill_id",
		"priority",
		"carrier_id",
		"name",
		"eta_hours",
		"reliability_pct",
	},
	"tracking": {
		"waybill_id",
		"sequence",
		"label",
		"recorded_at",
		"longitude",
		"latitude",
		"speed_kph",
		"anomaly",
	},
	"weather": {
		"origin",
		"destination",
		"sequence",
		"segment",
		"condition",
		"alert_level",
	},
}

var requiredImpactJSONFields = []string{
	"no_action_eta_hours",
	"post_action_eta_hours",
	"avoided_penalty_cents",
	"reassign_delta_cents",
	"handling_cost_cents",
}

func rejectMissingRequiredFields(raw []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	var collector issueCollector
	for _, field := range []string{
		"schema_version",
		"dataset_id",
		"waybills",
		"drivers",
		"waybill_candidates",
		"tracking",
		"weather",
	} {
		if missingJSONField(root, field) {
			collector.add("dataset", field, "required", "is required")
		}
	}
	for collection, fields := range requiredJSONFields {
		var records []map[string]json.RawMessage
		if err := json.Unmarshal(root[collection], &records); err != nil {
			continue
		}
		for index, record := range records {
			location := fmt.Sprintf("%s[%d]", collection, index)
			for _, field := range fields {
				if missingJSONField(record, field) {
					collector.add(location, field, "required", "is required")
				}
			}
			if collection == "waybills" {
				validateRequiredImpactJSONFields(&collector, location, record["impact"])
			}
		}
	}
	return collector.err()
}

func validateRequiredImpactJSONFields(
	collector *issueCollector,
	location string,
	raw json.RawMessage,
) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var impact map[string]json.RawMessage
	if err := json.Unmarshal(raw, &impact); err != nil {
		return
	}
	for _, field := range requiredImpactJSONFields {
		if missingJSONField(impact, field) {
			collector.add(location+".impact", field, "required", "is required")
		}
	}
}

func missingJSONField(
	object map[string]json.RawMessage,
	field string,
) bool {
	raw, exists := object[field]
	return !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func assignJSONLocations(draft *datasetDraft) {
	for index := range draft.Hubs {
		draft.Hubs[index].location = fmt.Sprintf("hubs[%d]", index)
	}
	for index := range draft.Vehicles {
		draft.Vehicles[index].location = fmt.Sprintf("vehicles[%d]", index)
	}
	for index := range draft.Routes {
		draft.Routes[index].location = fmt.Sprintf("routes[%d]", index)
	}
	for index := range draft.Waybills {
		draft.Waybills[index].location = fmt.Sprintf("waybills[%d]", index)
	}
	for index := range draft.Drivers {
		draft.Drivers[index].location = fmt.Sprintf("drivers[%d]", index)
	}
	for index := range draft.WaybillCandidates {
		draft.WaybillCandidates[index].location = fmt.Sprintf(
			"waybill_candidates[%d]",
			index,
		)
	}
	for index := range draft.Tracking {
		draft.Tracking[index].location = fmt.Sprintf("tracking[%d]", index)
	}
	for index := range draft.Weather {
		draft.Weather[index].location = fmt.Sprintf("weather[%d]", index)
	}
}
