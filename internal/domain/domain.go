package domain

import (
	"errors"
	"fmt"
	"regexp"
)

var ErrInvalidWaybillID = errors.New("invalid waybill id")

var waybillIDPattern = regexp.MustCompile(`^YD[0-9]{10}$`)

type RunID string
type IncidentID string
type WaybillID string
type DriverID string
type CarrierID string
type ApprovalID string
type EffectID string
type IdempotencyKey string

type Action string

const (
	ActionGetWaybill     Action = "tms.get_waybill"
	ActionGetTracking    Action = "tms.get_tracking"
	ActionGetDriver      Action = "tms.get_driver"
	ActionGetRoadWeather Action = "ext.get_road_weather"
	ActionReassign       Action = "tms.reassign"
	ActionCreateClaim    Action = "tms.create_claim"
	ActionSendSMS        Action = "notify.send_sms"
)

func (a Action) IsWrite() bool {
	switch a {
	case ActionReassign, ActionCreateClaim, ActionSendSMS:
		return true
	default:
		return false
	}
}

type RunStatus string

const (
	RunStarted          RunStatus = "started"
	RunInvestigating    RunStatus = "investigating"
	RunAwaitingApproval RunStatus = "awaiting_approval"
	RunExecuting        RunStatus = "executing"
	RunCompleted        RunStatus = "completed"
	RunRejected         RunStatus = "rejected"
	RunFailed           RunStatus = "failed"
)

type RunContext struct {
	RunID       RunID
	IncidentID  IncidentID
	WaybillID   WaybillID
	PlanVersion int
}

func ValidateWaybillID(id WaybillID) error {
	if !waybillIDPattern.MatchString(string(id)) {
		return fmt.Errorf("%w: must match YD followed by 10 digits", ErrInvalidWaybillID)
	}
	return nil
}
