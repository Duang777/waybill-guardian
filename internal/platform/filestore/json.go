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
	if err := rejectDuplicateTopLevelFields(raw); err != nil {
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

func rejectDuplicateTopLevelFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("json: top-level value must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("json: %w", err)
		}
		field, ok := token.(string)
		if !ok {
			return fmt.Errorf("json: object field name is invalid")
		}
		if _, exists := seen[field]; exists {
			return &ValidationError{Issues: []ValidationIssue{{
				Location: "json",
				Field:    field,
				Code:     "duplicate_field",
				Message:  "duplicate top-level field",
			}}}
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("json: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
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
