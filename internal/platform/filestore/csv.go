package filestore

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

var csvColumns = []string{
	"schema_version",
	"dataset_id",
	"record_type",
	"waybill_id",
	"origin",
	"destination",
	"cargo",
	"current_carrier_id",
	"driver_id",
	"status",
	"sla_hours",
	"shipper_phone",
	"priority",
	"carrier_id",
	"carrier_name",
	"eta_hours",
	"reliability_pct",
	"tracking_sequence",
	"label",
	"recorded_at",
	"longitude",
	"latitude",
	"speed_kph",
	"stop_hours",
	"anomaly",
	"driver_name",
	"driver_phone",
	"driver_plate",
	"continuous_drive_hours",
	"fatigue_alert",
	"weather_sequence",
	"segment",
	"condition",
	"alert_level",
	"origin_hub_id",
	"destination_hub_id",
	"route_id",
	"vehicle_id",
	"hub_id",
	"hub_name",
	"province",
	"city",
	"daily_capacity",
	"vehicle_plate",
	"vehicle_type",
	"load_capacity_tons",
	"distance_km",
	"standard_hours",
	"anomaly_type",
}

var csvFieldsByRecord = map[string]map[string]bool{
	"dataset": fieldSet(
		"schema_version",
		"dataset_id",
	),
	"hub": fieldSet(
		"hub_id",
		"hub_name",
		"province",
		"city",
		"longitude",
		"latitude",
		"daily_capacity",
	),
	"vehicle": fieldSet(
		"vehicle_id",
		"vehicle_plate",
		"vehicle_type",
		"load_capacity_tons",
	),
	"route": fieldSet(
		"route_id",
		"origin_hub_id",
		"destination_hub_id",
		"distance_km",
		"standard_hours",
	),
	"waybill": fieldSet(
		"waybill_id",
		"origin",
		"destination",
		"origin_hub_id",
		"destination_hub_id",
		"route_id",
		"vehicle_id",
		"cargo",
		"current_carrier_id",
		"driver_id",
		"status",
		"sla_hours",
		"shipper_phone",
	),
	"driver": fieldSet(
		"driver_id",
		"driver_name",
		"driver_phone",
		"driver_plate",
		"continuous_drive_hours",
		"fatigue_alert",
	),
	"candidate": fieldSet(
		"waybill_id",
		"priority",
		"carrier_id",
		"carrier_name",
		"eta_hours",
		"reliability_pct",
	),
	"tracking": fieldSet(
		"waybill_id",
		"tracking_sequence",
		"label",
		"recorded_at",
		"longitude",
		"latitude",
		"speed_kph",
		"stop_hours",
		"anomaly",
		"anomaly_type",
	),
	"weather": fieldSet(
		"origin",
		"destination",
		"route_id",
		"weather_sequence",
		"segment",
		"condition",
		"alert_level",
	),
}

var csvOptionalFields = fieldSet(
	"stop_hours",
	"origin_hub_id",
	"destination_hub_id",
	"route_id",
	"vehicle_id",
	"anomaly_type",
)

var csvExtensionFields = fieldSet(
	"origin_hub_id",
	"destination_hub_id",
	"route_id",
	"vehicle_id",
	"hub_id",
	"hub_name",
	"province",
	"city",
	"daily_capacity",
	"vehicle_plate",
	"vehicle_type",
	"load_capacity_tons",
	"distance_km",
	"standard_hours",
	"anomaly_type",
)

func parseCSV(raw []byte) (datasetDraft, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return datasetDraft{}, fmt.Errorf("csv: data is empty")
	}
	if !utf8.Valid(raw) {
		return datasetDraft{}, fmt.Errorf("csv: data is not valid UTF-8")
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return datasetDraft{}, fmt.Errorf("csv header: %s", csvErrorMessage(err))
	}
	index, err := validateCSVHeader(header)
	if err != nil {
		return datasetDraft{}, err
	}
	reader.FieldsPerRecord = len(header)

	var (
		draft       datasetDraft
		collector   issueCollector
		datasetRows int
		recordCount int
	)
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return datasetDraft{}, fmt.Errorf("csv: %s", csvErrorMessage(readErr))
		}
		recordCount++
		line, _ := reader.FieldPos(0)
		location := fmt.Sprintf("row %d", line)
		if recordCount > maxCSVRows {
			collector.add(location, "", "row_limit", "CSV exceeds %d data rows", maxCSVRows)
			break
		}
		values := make(map[string]string, len(index))
		for field, position := range index {
			values[field] = record[position]
		}
		recordType := values["record_type"]
		if recordType == "" {
			collector.add(location, "record_type", "required", "is required")
			continue
		}
		fields, ok := csvFieldsByRecord[recordType]
		if !ok {
			collector.add(
				location,
				"record_type",
				"unknown_record_type",
				"unknown record type %q",
				recordType,
			)
			continue
		}
		validateCSVRowShape(&collector, location, values, fields)
		ref := sourceRef{location: location}
		switch recordType {
		case "dataset":
			datasetRows++
			if datasetRows == 1 {
				draft.SchemaVersion = values["schema_version"]
				draft.DatasetID = values["dataset_id"]
			}
		case "hub":
			draft.Hubs = append(draft.Hubs, hubDraft{
				HubID:         values["hub_id"],
				Name:          values["hub_name"],
				Province:      values["province"],
				City:          values["city"],
				Longitude:     parseCSVFloat(&collector, location, "longitude", values["longitude"]),
				Latitude:      parseCSVFloat(&collector, location, "latitude", values["latitude"]),
				DailyCapacity: parseCSVInt(&collector, location, "daily_capacity", values["daily_capacity"]),
				sourceRef:     ref,
			})
		case "vehicle":
			draft.Vehicles = append(draft.Vehicles, vehicleDraft{
				VehicleID:        values["vehicle_id"],
				MaskedPlate:      values["vehicle_plate"],
				Type:             values["vehicle_type"],
				LoadCapacityTons: parseCSVFloat(&collector, location, "load_capacity_tons", values["load_capacity_tons"]),
				sourceRef:        ref,
			})
		case "route":
			draft.Routes = append(draft.Routes, routeDraft{
				RouteID:          values["route_id"],
				OriginHubID:      values["origin_hub_id"],
				DestinationHubID: values["destination_hub_id"],
				DistanceKM:       parseCSVInt(&collector, location, "distance_km", values["distance_km"]),
				StandardHours:    parseCSVInt(&collector, location, "standard_hours", values["standard_hours"]),
				sourceRef:        ref,
			})
		case "waybill":
			draft.Waybills = append(draft.Waybills, waybillDraft{
				WaybillID:        values["waybill_id"],
				Origin:           values["origin"],
				Destination:      values["destination"],
				OriginHubID:      values["origin_hub_id"],
				DestinationHubID: values["destination_hub_id"],
				RouteID:          values["route_id"],
				VehicleID:        values["vehicle_id"],
				Cargo:            values["cargo"],
				CurrentCarrierID: values["current_carrier_id"],
				DriverID:         values["driver_id"],
				Status:           values["status"],
				SLAHours:         parseCSVInt(&collector, location, "sla_hours", values["sla_hours"]),
				ShipperPhone:     values["shipper_phone"],
				sourceRef:        ref,
			})
		case "driver":
			draft.Drivers = append(draft.Drivers, driverDraft{
				DriverID:             values["driver_id"],
				Name:                 values["driver_name"],
				Phone:                values["driver_phone"],
				Plate:                values["driver_plate"],
				ContinuousDriveHours: parseCSVFloat(&collector, location, "continuous_drive_hours", values["continuous_drive_hours"]),
				FatigueAlert:         parseCSVBool(&collector, location, "fatigue_alert", values["fatigue_alert"]),
				sourceRef:            ref,
			})
		case "candidate":
			draft.WaybillCandidates = append(draft.WaybillCandidates, candidateDraft{
				WaybillID:      values["waybill_id"],
				Priority:       parseCSVInt(&collector, location, "priority", values["priority"]),
				CarrierID:      values["carrier_id"],
				Name:           values["carrier_name"],
				ETAHours:       parseCSVInt(&collector, location, "eta_hours", values["eta_hours"]),
				ReliabilityPct: parseCSVFloat(&collector, location, "reliability_pct", values["reliability_pct"]),
				sourceRef:      ref,
			})
		case "tracking":
			draft.Tracking = append(draft.Tracking, trackingDraft{
				WaybillID:   values["waybill_id"],
				Sequence:    parseCSVInt(&collector, location, "tracking_sequence", values["tracking_sequence"]),
				Label:       values["label"],
				RecordedAt:  values["recorded_at"],
				Longitude:   parseCSVFloat(&collector, location, "longitude", values["longitude"]),
				Latitude:    parseCSVFloat(&collector, location, "latitude", values["latitude"]),
				SpeedKPH:    parseCSVInt(&collector, location, "speed_kph", values["speed_kph"]),
				StopHours:   parseCSVOptionalFloat(&collector, location, "stop_hours", values["stop_hours"]),
				Anomaly:     parseCSVBool(&collector, location, "anomaly", values["anomaly"]),
				AnomalyType: values["anomaly_type"],
				sourceRef:   ref,
			})
		case "weather":
			draft.Weather = append(draft.Weather, weatherDraft{
				RouteID:     values["route_id"],
				Origin:      values["origin"],
				Destination: values["destination"],
				Sequence:    parseCSVInt(&collector, location, "weather_sequence", values["weather_sequence"]),
				Segment:     values["segment"],
				Condition:   values["condition"],
				AlertLevel:  values["alert_level"],
				sourceRef:   ref,
			})
		}
	}
	if datasetRows != 1 {
		collector.add(
			"csv",
			"record_type",
			"dataset_count",
			"requires exactly one dataset row; got %d",
			datasetRows,
		)
	}
	if err := collector.err(); err != nil {
		return datasetDraft{}, err
	}
	return draft, nil
}

func validateCSVHeader(header []string) (map[string]int, error) {
	known := fieldSet(csvColumns...)
	index := make(map[string]int, len(header))
	var collector issueCollector
	for position, field := range header {
		if field == "" {
			collector.add("header", "", "empty_column", "column %d has an empty name", position+1)
			continue
		}
		if _, exists := index[field]; exists {
			collector.add("header", field, "duplicate_column", "duplicate column")
			continue
		}
		index[field] = position
		if !known[field] {
			collector.add("header", field, "unknown_column", "unknown column")
		}
	}
	for _, field := range csvColumns {
		if _, exists := index[field]; !exists && !csvExtensionFields[field] {
			collector.add("header", field, "missing_column", "missing required column")
		}
	}
	if err := collector.err(); err != nil {
		return nil, err
	}
	return index, nil
}

func validateCSVRowShape(
	collector *issueCollector,
	location string,
	values map[string]string,
	fields map[string]bool,
) {
	for _, field := range csvColumns {
		if field == "record_type" {
			continue
		}
		value := values[field]
		if fields[field] {
			if value == "" && !csvOptionalFields[field] {
				collector.add(location, field, "required", "is required")
			}
			continue
		}
		if value != "" {
			collector.add(
				location,
				field,
				"unexpected_value",
				"must be empty for this record type",
			)
		}
	}
}

func parseCSVInt(
	collector *issueCollector,
	location string,
	field string,
	value string,
) int {
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		collector.add(location, field, "invalid_integer", "must be an integer")
		return 0
	}
	return parsed
}

func parseCSVFloat(
	collector *issueCollector,
	location string,
	field string,
	value string,
) float64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		collector.add(location, field, "invalid_number", "must be a finite number")
		return 0
	}
	return parsed
}

func parseCSVOptionalFloat(
	collector *issueCollector,
	location string,
	field string,
	value string,
) *float64 {
	if value == "" {
		return nil
	}
	parsed := parseCSVFloat(collector, location, field, value)
	return &parsed
}

func parseCSVBool(
	collector *issueCollector,
	location string,
	field string,
	value string,
) bool {
	switch value {
	case "true":
		return true
	case "false", "":
		return false
	default:
		collector.add(location, field, "invalid_boolean", `must be "true" or "false"`)
		return false
	}
}

func fieldSet(fields ...string) map[string]bool {
	set := make(map[string]bool, len(fields))
	for _, field := range fields {
		set[field] = true
	}
	return set
}

func csvErrorMessage(err error) string {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Sprintf("row %d: malformed CSV", parseErr.Line)
	}
	return "could not read CSV"
}
