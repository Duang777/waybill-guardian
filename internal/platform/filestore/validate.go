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
	hubs := validateHubs(&collector, draft.Hubs)
	vehicles := validateVehicles(&collector, draft.Vehicles)
	routes := validateRoutes(&collector, draft.Routes, hubs)
	hasNetwork := len(draft.Hubs) > 0 || len(draft.Vehicles) > 0 || len(draft.Routes) > 0
	if hasNetwork {
		if len(draft.Hubs) == 0 {
			collector.add("dataset", "hubs", "required", "network data requires hubs")
		}
		if len(draft.Vehicles) == 0 {
			collector.add("dataset", "vehicles", "required", "network data requires vehicles")
		}
		if len(draft.Routes) == 0 {
			collector.add("dataset", "routes", "required", "network data requires routes")
		}
	}

	waybills := make(map[string]waybillDraft, len(draft.Waybills))
	for _, waybill := range draft.Waybills {
		validateWaybill(&collector, waybill, hasNetwork, hubs, vehicles, routes)
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
	tracking := validateTracking(&collector, draft.Tracking, waybills, hasNetwork)
	weather := validateWeather(&collector, draft.Weather, hasNetwork, hubs, routes)

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

func validateWaybill(
	collector *issueCollector,
	waybill waybillDraft,
	hasNetwork bool,
	hubs map[string]hubDraft,
	vehicles map[string]vehicleDraft,
	routes map[string]routeDraft,
) {
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
	if hasNetwork {
		validateIdentifier(collector, waybill.location, "origin_hub_id", waybill.OriginHubID)
		validateIdentifier(
			collector,
			waybill.location,
			"destination_hub_id",
			waybill.DestinationHubID,
		)
		validateIdentifier(collector, waybill.location, "route_id", waybill.RouteID)
		validateIdentifier(collector, waybill.location, "vehicle_id", waybill.VehicleID)
		validateNetworkReferences(collector, waybill, hubs, vehicles, routes)
	} else {
		for field, value := range map[string]string{
			"origin_hub_id":      waybill.OriginHubID,
			"destination_hub_id": waybill.DestinationHubID,
			"route_id":           waybill.RouteID,
			"vehicle_id":         waybill.VehicleID,
		} {
			if value != "" {
				collector.add(
					waybill.location,
					field,
					"network_required",
					"requires hubs, vehicles, and routes",
				)
			}
		}
	}
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

func validateHubs(
	collector *issueCollector,
	items []hubDraft,
) map[string]hubDraft {
	result := make(map[string]hubDraft, len(items))
	for _, hub := range items {
		validateIdentifier(collector, hub.location, "hub_id", hub.HubID)
		validateRequiredText(collector, hub.location, "name", hub.Name)
		validateRequiredText(collector, hub.location, "province", hub.Province)
		validateRequiredText(collector, hub.location, "city", hub.City)
		validateCoordinate(
			collector,
			hub.location,
			"longitude",
			hub.Longitude,
			-180,
			180,
		)
		validateCoordinate(
			collector,
			hub.location,
			"latitude",
			hub.Latitude,
			-90,
			90,
		)
		if hub.DailyCapacity <= 0 {
			collector.add(
				hub.location,
				"daily_capacity",
				"invalid_capacity",
				"must be greater than zero",
			)
		}
		if _, exists := result[hub.HubID]; exists {
			collector.add(
				hub.location,
				"hub_id",
				"duplicate",
				"duplicates another hub",
			)
		} else {
			result[hub.HubID] = hub
		}
	}
	return result
}

func validateVehicles(
	collector *issueCollector,
	items []vehicleDraft,
) map[string]vehicleDraft {
	result := make(map[string]vehicleDraft, len(items))
	for _, vehicle := range items {
		validateIdentifier(
			collector,
			vehicle.location,
			"vehicle_id",
			vehicle.VehicleID,
		)
		validateRequiredText(
			collector,
			vehicle.location,
			"masked_plate",
			vehicle.MaskedPlate,
		)
		validateRequiredText(collector, vehicle.location, "type", vehicle.Type)
		if !finite(vehicle.LoadCapacityTons) || vehicle.LoadCapacityTons <= 0 {
			collector.add(
				vehicle.location,
				"load_capacity_tons",
				"invalid_capacity",
				"must be a finite number greater than zero",
			)
		}
		if _, exists := result[vehicle.VehicleID]; exists {
			collector.add(
				vehicle.location,
				"vehicle_id",
				"duplicate",
				"duplicates another vehicle",
			)
		} else {
			result[vehicle.VehicleID] = vehicle
		}
	}
	return result
}

func validateRoutes(
	collector *issueCollector,
	items []routeDraft,
	hubs map[string]hubDraft,
) map[string]routeDraft {
	result := make(map[string]routeDraft, len(items))
	for _, route := range items {
		validateIdentifier(collector, route.location, "route_id", route.RouteID)
		validateIdentifier(
			collector,
			route.location,
			"origin_hub_id",
			route.OriginHubID,
		)
		validateIdentifier(
			collector,
			route.location,
			"destination_hub_id",
			route.DestinationHubID,
		)
		if route.OriginHubID == route.DestinationHubID && route.OriginHubID != "" {
			collector.add(
				route.location,
				"destination_hub_id",
				"same_endpoint",
				"must differ from origin_hub_id",
			)
		}
		if _, exists := hubs[route.OriginHubID]; !exists {
			collector.add(
				route.location,
				"origin_hub_id",
				"unknown_reference",
				"references an unknown hub",
			)
		}
		if _, exists := hubs[route.DestinationHubID]; !exists {
			collector.add(
				route.location,
				"destination_hub_id",
				"unknown_reference",
				"references an unknown hub",
			)
		}
		if route.DistanceKM <= 0 {
			collector.add(
				route.location,
				"distance_km",
				"invalid_distance",
				"must be greater than zero",
			)
		}
		if route.StandardHours <= 0 {
			collector.add(
				route.location,
				"standard_hours",
				"invalid_duration",
				"must be greater than zero",
			)
		}
		if _, exists := result[route.RouteID]; exists {
			collector.add(
				route.location,
				"route_id",
				"duplicate",
				"duplicates another route",
			)
		} else {
			result[route.RouteID] = route
		}
	}
	return result
}

func validateNetworkReferences(
	collector *issueCollector,
	waybill waybillDraft,
	hubs map[string]hubDraft,
	vehicles map[string]vehicleDraft,
	routes map[string]routeDraft,
) {
	origin, originOK := hubs[waybill.OriginHubID]
	if !originOK {
		collector.add(
			waybill.location,
			"origin_hub_id",
			"unknown_reference",
			"references an unknown hub",
		)
	} else if origin.City != waybill.Origin {
		collector.add(
			waybill.location,
			"origin",
			"hub_mismatch",
			"must match the origin hub city",
		)
	}
	destination, destinationOK := hubs[waybill.DestinationHubID]
	if !destinationOK {
		collector.add(
			waybill.location,
			"destination_hub_id",
			"unknown_reference",
			"references an unknown hub",
		)
	} else if destination.City != waybill.Destination {
		collector.add(
			waybill.location,
			"destination",
			"hub_mismatch",
			"must match the destination hub city",
		)
	}
	if _, exists := vehicles[waybill.VehicleID]; !exists {
		collector.add(
			waybill.location,
			"vehicle_id",
			"unknown_reference",
			"references an unknown vehicle",
		)
	}
	route, exists := routes[waybill.RouteID]
	if !exists {
		collector.add(
			waybill.location,
			"route_id",
			"unknown_reference",
			"references an unknown route",
		)
		return
	}
	if route.OriginHubID != waybill.OriginHubID ||
		route.DestinationHubID != waybill.DestinationHubID {
		collector.add(
			waybill.location,
			"route_id",
			"endpoint_mismatch",
			"route endpoints must match the waybill hubs",
		)
	}
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
	hasNetwork bool,
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
		if point.Anomaly && hasNetwork {
			validateRequiredText(
				collector,
				point.location,
				"anomaly_type",
				point.AnomalyType,
			)
		} else if point.AnomalyType != "" {
			collector.add(
				point.location,
				"anomaly_type",
				"unexpected_value",
				"must be empty when anomaly is false",
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
	hasNetwork bool,
	hubs map[string]hubDraft,
	routes map[string]routeDraft,
) map[routeKey][]weatherDraft {
	grouped := make(map[routeKey][]weatherDraft)
	aliases := make(map[string]routeKey)
	for _, item := range items {
		if hasNetwork {
			validateIdentifier(collector, item.location, "route_id", item.RouteID)
			route, exists := routes[item.RouteID]
			if !exists {
				collector.add(
					item.location,
					"route_id",
					"unknown_reference",
					"references an unknown route",
				)
			} else {
				origin, originOK := hubs[route.OriginHubID]
				destination, destinationOK := hubs[route.DestinationHubID]
				if originOK && destinationOK &&
					(origin.City != item.Origin || destination.City != item.Destination) {
					collector.add(
						item.location,
						"route_id",
						"endpoint_mismatch",
						"route hubs must match the weather origin and destination",
					)
				}
			}
		} else if item.RouteID != "" {
			collector.add(
				item.location,
				"route_id",
				"network_required",
				"requires hubs, vehicles, and routes",
			)
		}
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

func validateCoordinate(
	collector *issueCollector,
	location string,
	field string,
	value float64,
	minimum float64,
	maximum float64,
) {
	if !finite(value) || value < minimum || value > maximum {
		collector.add(
			location,
			field,
			"out_of_range",
			"must be between %v and %v",
			minimum,
			maximum,
		)
	}
}
