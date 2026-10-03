package events

import (
	"errors"
	"fmt"
)

type DecodeCode string

const (
	DecodeInvalidContentType DecodeCode = "invalid_content_type"
	DecodeBodyTooLarge       DecodeCode = "body_too_large"
	DecodeInvalidJSON        DecodeCode = "invalid_json"
	DecodeInvalidCloudEvent  DecodeCode = "invalid_cloudevent"
	DecodeUnsupportedType    DecodeCode = "unsupported_event_type"
	DecodeUnsupportedSchema  DecodeCode = "unsupported_event_schema"
	DecodeInvalidData        DecodeCode = "invalid_event_data"
)

type DecodeError struct {
	Code DecodeCode
	Err  error
}

func (e *DecodeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *DecodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func decodeError(code DecodeCode, format string, args ...any) error {
	return &DecodeError{Code: code, Err: fmt.Errorf(format, args...)}
}

func DecodeErrorCode(err error) (DecodeCode, bool) {
	var value *DecodeError
	if !errors.As(err, &value) {
		return "", false
	}
	return value.Code, true
}

var (
	ErrEventIdentityConflict    = errors.New("event identity has different content")
	ErrLegacyEventIdentity      = errors.New("event identity uses a legacy hash profile")
	ErrIncidentIdentityConflict = errors.New("incident identity belongs to another waybill")
	ErrEventsUnavailable        = errors.New("event ingestion requires PostgreSQL")
)
