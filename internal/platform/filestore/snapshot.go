package filestore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

type snapshot struct {
	waybills    map[domain.WaybillID]platform.Waybill
	tracking    map[domain.WaybillID][]platform.TrackPoint
	drivers     map[domain.DriverID]platform.Driver
	weather     map[routeKey][]platform.RoadWeather
	routeLookup map[string]routeKey
	catalog     []platform.WaybillSummary
	hubs        []platform.Hub
	vehicles    []platform.Vehicle
	routes      []platform.Route
}

func buildSnapshot(draft datasetDraft) (*snapshot, Stats) {
	result := &snapshot{
		waybills:    make(map[domain.WaybillID]platform.Waybill, len(draft.Waybills)),
		tracking:    make(map[domain.WaybillID][]platform.TrackPoint, len(draft.Waybills)),
		drivers:     make(map[domain.DriverID]platform.Driver, len(draft.Drivers)),
		weather:     make(map[routeKey][]platform.RoadWeather),
		routeLookup: make(map[string]routeKey),
	}
	for _, hub := range draft.Hubs {
		result.hubs = append(result.hubs, platform.Hub{
			ID:            platform.HubID(hub.HubID),
			Name:          hub.Name,
			Province:      hub.Province,
			City:          hub.City,
			Longitude:     hub.Longitude,
			Latitude:      hub.Latitude,
			DailyCapacity: hub.DailyCapacity,
		})
	}
	sort.Slice(result.hubs, func(left, right int) bool {
		return result.hubs[left].ID < result.hubs[right].ID
	})
	for _, vehicle := range draft.Vehicles {
		result.vehicles = append(result.vehicles, platform.Vehicle{
			ID:               platform.VehicleID(vehicle.VehicleID),
			MaskedPlate:      vehicle.MaskedPlate,
			Type:             vehicle.Type,
			LoadCapacityTons: vehicle.LoadCapacityTons,
		})
	}
	sort.Slice(result.vehicles, func(left, right int) bool {
		return result.vehicles[left].ID < result.vehicles[right].ID
	})
	for _, route := range draft.Routes {
		result.routes = append(result.routes, platform.Route{
			ID:               platform.RouteID(route.RouteID),
			OriginHubID:      platform.HubID(route.OriginHubID),
			DestinationHubID: platform.HubID(route.DestinationHubID),
			DistanceKM:       route.DistanceKM,
			StandardHours:    route.StandardHours,
		})
	}
	sort.Slice(result.routes, func(left, right int) bool {
		return result.routes[left].ID < result.routes[right].ID
	})
	for _, driver := range draft.Drivers {
		id := domain.DriverID(driver.DriverID)
		result.drivers[id] = platform.Driver{
			ID:                 id,
			Name:               driver.Name,
			Phone:              driver.Phone,
			Plate:              driver.Plate,
			ContinuousDriveHrs: driver.ContinuousDriveHours,
			FatigueAlert:       driver.FatigueAlert,
		}
	}

	candidates := make(map[string][]candidateDraft)
	for _, candidate := range draft.WaybillCandidates {
		candidates[candidate.WaybillID] = append(
			candidates[candidate.WaybillID],
			candidate,
		)
	}
	for waybillID := range candidates {
		sort.Slice(candidates[waybillID], func(left, right int) bool {
			return candidates[waybillID][left].Priority <
				candidates[waybillID][right].Priority
		})
	}
	for _, waybill := range draft.Waybills {
		id := domain.WaybillID(waybill.WaybillID)
		carriers := make([]platform.Carrier, 0, len(candidates[waybill.WaybillID]))
		for _, candidate := range candidates[waybill.WaybillID] {
			carriers = append(carriers, platform.Carrier{
				ID:             domain.CarrierID(candidate.CarrierID),
				Name:           candidate.Name,
				ETAHours:       candidate.ETAHours,
				ReliabilityPct: candidate.ReliabilityPct,
			})
		}
		result.waybills[id] = platform.Waybill{
			ID:                id,
			Origin:            waybill.Origin,
			Destination:       waybill.Destination,
			OriginHubID:       platform.HubID(waybill.OriginHubID),
			DestinationHubID:  platform.HubID(waybill.DestinationHubID),
			RouteID:           platform.RouteID(waybill.RouteID),
			VehicleID:         platform.VehicleID(waybill.VehicleID),
			Cargo:             waybill.Cargo,
			CarrierID:         domain.CarrierID(waybill.CurrentCarrierID),
			DriverID:          domain.DriverID(waybill.DriverID),
			Status:            waybill.Status,
			SLAHours:          waybill.SLAHours,
			ShipperPhone:      waybill.ShipperPhone,
			CandidateCarriers: carriers,
		}
	}

	tracks := make(map[string][]trackingDraft)
	for _, point := range draft.Tracking {
		tracks[point.WaybillID] = append(tracks[point.WaybillID], point)
	}
	stats := Stats{Waybills: len(draft.Waybills)}
	for _, waybill := range draft.Waybills {
		points := tracks[waybill.WaybillID]
		sort.Slice(points, func(left, right int) bool {
			return points[left].Sequence < points[right].Sequence
		})
		converted := make([]platform.TrackPoint, 0, len(points))
		summary := platform.WaybillSummary{
			WaybillID:        domain.WaybillID(waybill.WaybillID),
			Origin:           waybill.Origin,
			Destination:      waybill.Destination,
			OriginHubID:      platform.HubID(waybill.OriginHubID),
			DestinationHubID: platform.HubID(waybill.DestinationHubID),
			RouteID:          platform.RouteID(waybill.RouteID),
			Status:           waybill.Status,
		}
		for _, point := range points {
			stopHours := 0.0
			if point.StopHours != nil {
				stopHours = *point.StopHours
			}
			converted = append(converted, platform.TrackPoint{
				Label:       point.Label,
				RecordedAt:  point.RecordedAt,
				Longitude:   point.Longitude,
				Latitude:    point.Latitude,
				SpeedKPH:    point.SpeedKPH,
				StopHours:   stopHours,
				Anomaly:     point.Anomaly,
				AnomalyType: point.AnomalyType,
			})
			recordedAt, _ := time.Parse(time.RFC3339, point.RecordedAt)
			summary.LastRecordedAt = recordedAt
			if point.Anomaly && !summary.HasAnomaly {
				summary.HasAnomaly = true
				summary.AnomalyLabel = point.Label
				summary.AnomalyType = point.AnomalyType
			}
		}
		if summary.HasAnomaly {
			stats.Anomalies++
		}
		result.tracking[summary.WaybillID] = converted
		result.catalog = append(result.catalog, summary)
	}
	sort.Slice(result.catalog, func(left, right int) bool {
		return result.catalog[left].WaybillID < result.catalog[right].WaybillID
	})

	routes := make(map[routeKey][]weatherDraft)
	for _, item := range draft.Weather {
		key := routeKey{origin: item.Origin, destination: item.Destination}
		routes[key] = append(routes[key], item)
	}
	for key, items := range routes {
		sort.Slice(items, func(left, right int) bool {
			return items[left].Sequence < items[right].Sequence
		})
		converted := make([]platform.RoadWeather, 0, len(items))
		for _, item := range items {
			converted = append(converted, platform.RoadWeather{
				Segment:    item.Segment,
				Condition:  item.Condition,
				AlertLevel: item.AlertLevel,
			})
		}
		result.weather[key] = converted
		result.routeLookup[routeAlias(key)] = key
	}
	stats.Hubs = len(result.hubs)
	stats.Vehicles = len(result.vehicles)
	stats.Routes = len(result.routes)
	return result, stats
}

func (s *snapshot) readSet() platform.ReadSet {
	return platform.ReadSet{
		TMS:     s,
		Weather: s,
		Catalog: s,
		Network: s,
	}
}

func (s *snapshot) GetWaybill(
	ctx context.Context,
	request platform.GetWaybillRequest,
) (platform.Waybill, error) {
	if err := ctx.Err(); err != nil {
		return platform.Waybill{}, err
	}
	waybill, ok := s.waybills[request.WaybillID]
	if !ok {
		return platform.Waybill{}, fmt.Errorf(
			"%w: waybill %q",
			platform.ErrNotFound,
			request.WaybillID,
		)
	}
	return cloneWaybill(waybill), nil
}

func (s *snapshot) GetTracking(
	ctx context.Context,
	request platform.GetTrackingRequest,
) ([]platform.TrackPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	points, ok := s.tracking[request.WaybillID]
	if !ok {
		return nil, fmt.Errorf(
			"%w: tracking for waybill %q",
			platform.ErrNotFound,
			request.WaybillID,
		)
	}
	return append([]platform.TrackPoint(nil), points...), nil
}

func (s *snapshot) GetDriver(
	ctx context.Context,
	request platform.GetDriverRequest,
) (platform.Driver, error) {
	if err := ctx.Err(); err != nil {
		return platform.Driver{}, err
	}
	driver, ok := s.drivers[request.DriverID]
	if !ok {
		return platform.Driver{}, fmt.Errorf(
			"%w: driver %q",
			platform.ErrNotFound,
			request.DriverID,
		)
	}
	return driver, nil
}

func (s *snapshot) GetRoadWeather(
	ctx context.Context,
	request platform.GetRoadWeatherRequest,
) ([]platform.RoadWeather, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, ok := s.routeLookup[request.Route]
	if !ok {
		return nil, fmt.Errorf(
			"%w: weather for route %q",
			platform.ErrNotFound,
			request.Route,
		)
	}
	weather := s.weather[key]
	return append([]platform.RoadWeather(nil), weather...), nil
}

func (s *snapshot) ListWaybills(
	ctx context.Context,
) ([]platform.WaybillSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]platform.WaybillSummary(nil), s.catalog...), nil
}

func (s *snapshot) ListHubs(ctx context.Context) ([]platform.Hub, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]platform.Hub(nil), s.hubs...), nil
}

func (s *snapshot) ListVehicles(ctx context.Context) ([]platform.Vehicle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]platform.Vehicle(nil), s.vehicles...), nil
}

func (s *snapshot) ListRoutes(ctx context.Context) ([]platform.Route, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]platform.Route(nil), s.routes...), nil
}

func routeAlias(key routeKey) string {
	return key.origin + "-" + key.destination
}

func cloneWaybill(waybill platform.Waybill) platform.Waybill {
	waybill.CandidateCarriers = append(
		[]platform.Carrier(nil),
		waybill.CandidateCarriers...,
	)
	return waybill
}

var (
	_ platform.TMSReader      = (*snapshot)(nil)
	_ platform.WeatherReader  = (*snapshot)(nil)
	_ platform.WaybillCatalog = (*snapshot)(nil)
	_ platform.NetworkCatalog = (*snapshot)(nil)
)
