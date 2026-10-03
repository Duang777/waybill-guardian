package audit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func MaskPhone(value string) string {
	if len(value) < 7 {
		return strings.Repeat("*", len(value))
	}
	return value[:3] + "****" + value[len(value)-4:]
}

func MaskPlate(value string) string {
	count := utf8.RuneCountInString(value)
	if count <= 2 {
		return strings.Repeat("*", count)
	}
	runes := []rune(value)
	return string(runes[:2]) + strings.Repeat("*", count-3) + string(runes[count-1:])
}

func Redact(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal audit payload: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("normalize audit payload: %w", err)
	}
	redactValue(decoded)
	result, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("marshal redacted audit payload: %w", err)
	}
	return result, nil
}

func redactValue(value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			redactValue(item)
		}
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(key) {
			case "phone", "shipper_phone":
				if text, ok := item.(string); ok {
					typed[key] = MaskPhone(text)
				}
			case "plate", "license_plate":
				if text, ok := item.(string); ok {
					typed[key] = MaskPlate(text)
				}
			default:
				redactValue(item)
			}
		}
	}
}
