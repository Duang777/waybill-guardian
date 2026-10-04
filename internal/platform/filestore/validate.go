package filestore

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

func validateAndBuild(draft datasetDraft) (*snapshot, Stats, error) {
	var collector issueCollector
	validateDataset(&collector, draft)

	waybills := make(map[string]waybillDraft, len(draft.Waybills))
	for _, waybill := range draft.Waybills {
		validateWaybill(&collector, waybill)
		if _, exists := waybills[waybill.WaybillID]; exists {
			collector.add(
				waybill.location,
				"waybill_id",
				"duplicate",
				"duplicates another waybill",
			)
		} else {
			waybills[waybill.WaybillID] = waybill
		}
	}

	drivers := make(map[string]driverDraft, len(draft.Drivers))
	for _, driver := range draft.Drivers {
		validateDriver(&collector, driver)
		if _, exists := drivers[driver.DriverID]; exists {
			collector.add(
				driver.location,
				"driver_id",
				"duplicate",
				"duplicates another driver",
			)
		} else {
			drivers[driver.DriverID] = driver
		}
	}

	candidates := validateCandidates(&collector, draft.WaybillCandidates, waybills)
	tracking := validateTracking(&collector, draft.Tracking, waybills)
	weather := validateWeather(&collector, draft.Weather)

	for _, waybill := range draft.Waybills {
		if _, exists := drivers[waybill.DriverID]; !exists {
			collector.add(
				waybill.location,
				"driver_id",
				"unknown_reference",
				"references an unknown driver",
			)
		}
		if len(candidates[waybill.WaybillID]) == 0 {
			collector.add(
				waybill.location,
				"waybill_id",
				"missing_candidates",
				"must have at least one candidate carrier",
			)
		}
		if len(tracking[waybill.WaybillID]) == 0 {
			collector.add(
				waybill.location,
				"waybill_id",
				"missing_tracking",
				"must have at least one tracking point",
			)
		}
		key := routeKey{origin: waybill.Origin, destination: waybill.Destination}
		if len(weather[key]) == 0 {
			collector.add(
				waybill.location,
				"waybill_id",
				"missing_weather",
				"must have weather for its route",
			)
		}
	}
	if err := collector.err(); err != nil {
		return nil, Stats{}, err
	}
	result, stats := buildSnapshot(draft)
	return result, stats, nil
}

func validateDataset(collector *issueCollector, draft datasetDraft) {
	if draft.SchemaVersion != schemaVersionV1 {
		collector.add(
			"dataset",
			"schema_version",
			"unsupported_version",
			"must be %q",
			schemaVersionV1,
		)
	}
	validateIdentifier(
		collector,
		"dataset",
		"dataset_id",
		draft.DatasetID,
	)
	if len(draft.Waybills) == 0 {
		collector.add(
			"dataset",
			"waybills",
			"required",
			"must contain at least one waybill",
		)
	}
}

func validateWaybill(collector *issueCollector, waybill waybillDraft) {
	if err := domain.ValidateWaybillID(domain.WaybillID(waybill.WaybillID)); err != nil {
		collector.add(
			waybill.location,
			"waybill_id",
			"invalid_waybill_id",
			"must match YD followed by 10 digits",
		)
	}
	validateRequiredText(collector, waybill.location, "origin", waybill.Origin)
	validateRequiredText(
		collector,
		waybill.location,
		"destination",
		waybill.Destination,
	)
	validateRequiredText(collector, waybill.location, "cargo", waybill.Cargo)
	validateIdentifier(
		collector,
		waybill.location,
		"current_carrier_id",
		waybill.CurrentCarrierID,
	)
	validateIdentifier(
		collector,
		waybill.location,
		"driver_id",
		waybill.DriverID,
	)
	validateRequiredText(collector, waybill.location, "status", waybill.Status)
	if waybill.SLAHours < 0 {
		collector.add(
			waybill.location,
			"sla_hours",
			"negative",
			"must not be negative",
		)
	}
	validateRequiredText(
		collector,
		waybill.location,
		"shipper_phone",
		waybill.ShipperPhone,
	)
}

func validateDriver(collector *issueCollector, driver driverDraft) {
	validateIdentifier(
		collector,
		driver.location,
		"driver_id",
		driver.DriverID,
	)
	validateRequiredText(collector, driver.location, "name", driver.Name)
	validateRequiredText(collector, driver.location, "phone", driver.Phone)
	validateRequiredText(collector, driver.location, "plate", driver.Plate)
	if !finite(driver.ContinuousDriveHours) || driver.ContinuousDriveHours < 0 {
		collector.add(
			driver.location,
			"continuous_drive_hours",
			"invalid_duration",
			"must be a finite non-negative number",
		)
	}
}

func validateCandidates(
	collector *issueCollector,
	items []candidateDraft,
	waybills map[string]waybillDraft,
) map[string][]candidateDraft {
	grouped := make(map[string][]candidateDraft)
	relations := make(map[string]struct{})
	for _, candidate := range items {
		validateWaybillReference(
			collector,
			candidate.location,
			candidate.WaybillID,
			waybills,
		)
		validateIdentifier(
			collector,
			candidate.location,
			"carrier_id",
			candidate.CarrierID,
		)
		validateRequiredText(
			collector,
			candidate.location,
			"name",
			candidate.Name,
		)
		if candidate.Priority <= 0 {
			collector.add(
				candidate.location,
				"priority",
				"invalid_priority",
				"must be greater than zero",
			)
		}
		if candidate.ETAHours < 0 {
			collector.add(
				candidate.location,
				"eta_hours",
				"negative",
				"must not be negative",
			)
		}
		if !finite(candidate.ReliabilityPct) ||
			candidate.ReliabilityPct < 0 ||
			candidate.ReliabilityPct > 100 {
			collector.add(
				candidate.location,
				"reliability_pct",
				"out_of_range",
				"must be between 0 and 100",
			)
		}
		relation := candidate.WaybillID + "\x00" + candidate.CarrierID
		if _, exists := relations[relation]; exists {
			collector.add(
				candidate.location,
				"carrier_id",
				"duplicate_relation",
				"duplicates another candidate for this waybill",
			)
		} else {
			relations[relation] = struct{}{}
		}
		grouped[candidate.WaybillID] = append(
			grouped[candidate.WaybillID],
			candidate,
		)
	}
	for _, candidates := range grouped {
		sorted := append([]candidateDraft(nil), candidates...)
		sort.Slice(sorted, func(left, right int) bool {
			return sorted[left].Priority < sorted[right].Priority
		})
		for index, candidate := range sorted {
			if candidate.Priority != index+1 {
				collector.add(
					candidate.location,
					"priority",
					"non_contiguous",
					"must form a contiguous sequence starting at 1",
				)
			}
		}
	}
	return grouped
}

func validateTracking(
	collector *issueCollector,
	items []trackingDraft,
	waybills map[string]waybillDraft,
) map[string][]trackingDraft {
	grouped := make(map[string][]trackingDraft)
	for _, point := range items {
		validateWaybillReference(
			collector,
			point.location,
			point.WaybillID,
			waybills,
		)
		if point.Sequence <= 0 {
			collector.add(
				point.location,
				"sequence",
				"invalid_sequence",
				"must be greater than zero",
			)
		}
		validateRequiredText(collector, point.location, "label", point.Label)
		if _, err := time.Parse(time.RFC3339, point.RecordedAt); err != nil {
			collector.add(
				point.location,
				"recorded_at",
				"invalid_time",
				"must use RFC3339",
			)
		}
		if !finite(point.Longitude) || point.Longitude < -180 || point.Longitude > 180 {
			collector.add(
				point.location,
				"longitude",
				"out_of_range",
				"must be between -180 and 180",
			)
		}
		if !finite(point.Latitude) || point.Latitude < -90 || point.Latitude > 90 {
			collector.add(
				point.location,
				"latitude",
				"out_of_range",
				"must be between -90 and 90",
			)
		}
		if point.SpeedKPH < 0 {
			collector.add(
				point.location,
				"speed_kph",
				"negative",
				"must not be negative",
			)
		}
		if point.StopHours != nil &&
			(!finite(*point.StopHours) || *point.StopHours < 0) {
			collector.add(
				point.location,
				"stop_hours",
				"invalid_duration",
				"must be a finite non-negative number",
			)
		}
		grouped[point.WaybillID] = append(grouped[point.WaybillID], point)
	}
	for _, points := range grouped {
		sorted := append([]trackingDraft(nil), points...)
		sort.Slice(sorted, func(left, right int) bool {
			return sorted[left].Sequence < sorted[right].Sequence
		})
		var previous time.Time
		for index, point := range sorted {
			if point.Sequence != index+1 {
				collector.add(
					point.location,
					"sequence",
					"non_contiguous",
					"must form a contiguous sequence starting at 1",
				)
			}
			recordedAt, err := time.Parse(time.RFC3339, point.RecordedAt)
			if err != nil {
				continue
			}
			if !previous.IsZero() && !recordedAt.After(previous) {
				collector.add(
					point.location,
					"recorded_at",
					"non_increasing",
					"must be later than the previous point for the waybill",
				)
			}
			previous = recordedAt
		}
	}
	return grouped
}

func validateWeather(
	collector *issueCollector,
	items []weatherDraft,
) map[routeKey][]weatherDraft {
	grouped := make(map[routeKey][]weatherDraft)
	aliases := make(map[string]routeKey)
	for _, item := range items {
		validateRequiredText(collector, item.location, "origin", item.Origin)
		validateRequiredText(
			collector,
			item.location,
			"destination",
			item.Destination,
		)
		if item.Sequence <= 0 {
			collector.add(
				item.location,
				"sequence",
				"invalid_sequence",
				"must be greater than zero",
			)
		}
		validateRequiredText(collector, item.location, "segment", item.Segment)
		validateRequiredText(
			collector,
			item.location,
			"condition",
			item.Condition,
		)
		validateRequiredText(
			collector,
			item.location,
			"alert_level",
			item.AlertLevel,
		)
		key := routeKey{origin: item.Origin, destination: item.Destination}
		alias := routeAlias(key)
		if existing, exists := aliases[alias]; exists && existing != key {
			collector.add(
				item.location,
				"origin",
				"ambiguous_route",
				"route conflicts with another origin and destination pair",
			)
		} else {
			aliases[alias] = key
		}
		grouped[key] = append(grouped[key], item)
	}
	for _, route := range grouped {
		sorted := append([]weatherDraft(nil), route...)
		sort.Slice(sorted, func(left, right int) bool {
			return sorted[left].Sequence < sorted[right].Sequence
		})
		for index, item := range sorted {
			if item.Sequence != index+1 {
				collector.add(
					item.location,
					"sequence",
					"non_contiguous",
					"must form a contiguous sequence starting at 1",
				)
			}
		}
	}
	return grouped
}

func validateWaybillReference(
	collector *issueCollector,
	location string,
	waybillID string,
	waybills map[string]waybillDraft,
) {
	if err := domain.ValidateWaybillID(domain.WaybillID(waybillID)); err != nil {
		collector.add(
			location,
			"waybill_id",
			"invalid_waybill_id",
			"must match YD followed by 10 digits",
		)
		return
	}
	if _, exists := waybills[waybillID]; !exists {
		collector.add(
			location,
			"waybill_id",
			"unknown_reference",
			"references an unknown waybill",
		)
	}
}

func validateIdentifier(
	collector *issueCollector,
	location string,
	field string,
	value string,
) {
	if value == "" {
		collector.add(location, field, "required", "is required")
		return
	}
	if strings.TrimSpace(value) != value {
		collector.add(
			location,
			field,
			"surrounding_whitespace",
			"must not have leading or trailing whitespace",
		)
		return
	}
	if !identifierPattern.MatchString(value) {
		collector.add(
			location,
			field,
			"invalid_identifier",
			"must contain 1 to 64 letters, digits, dots, underscores, or hyphens",
		)
	}
}

func validateRequiredText(
	collector *issueCollector,
	location string,
	field string,
	value string,
) {
	if strings.TrimSpace(value) == "" {
		collector.add(location, field, "required", "is required")
	}
}

func finite(value float64) bool {
	return !math.IsInf(value, 0) && !math.IsNaN(value)
}
