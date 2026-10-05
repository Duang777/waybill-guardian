package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultWaybillCount = 200
	defaultOutputPath   = "data/simulated/waybills-v1.json"
	trackingPointCount  = 7
)

type hubSeed struct {
	City      string
	Province  string
	Longitude float64
	Latitude  float64
}

type dataset struct {
	SchemaVersion     string            `json:"schema_version"`
	DatasetID         string            `json:"dataset_id"`
	Hubs              []hubRecord       `json:"hubs"`
	Vehicles          []vehicleRecord   `json:"vehicles"`
	Routes            []routeRecord     `json:"routes"`
	Waybills          []waybillRecord   `json:"waybills"`
	Drivers           []driverRecord    `json:"drivers"`
	WaybillCandidates []candidateRecord `json:"waybill_candidates"`
	Tracking          []trackingRecord  `json:"tracking"`
	Weather           []weatherRecord   `json:"weather"`
}

type hubRecord struct {
	HubID         string  `json:"hub_id"`
	Name          string  `json:"name"`
	Province      string  `json:"province"`
	City          string  `json:"city"`
	Longitude     float64 `json:"longitude"`
	Latitude      float64 `json:"latitude"`
	DailyCapacity int     `json:"daily_capacity"`
}

type vehicleRecord struct {
	VehicleID        string  `json:"vehicle_id"`
	MaskedPlate      string  `json:"masked_plate"`
	Type             string  `json:"type"`
	LoadCapacityTons float64 `json:"load_capacity_tons"`
}

type routeRecord struct {
	RouteID          string `json:"route_id"`
	OriginHubID      string `json:"origin_hub_id"`
	DestinationHubID string `json:"destination_hub_id"`
	DistanceKM       int    `json:"distance_km"`
	StandardHours    int    `json:"standard_hours"`
}

type waybillRecord struct {
	WaybillID        string        `json:"waybill_id"`
	Origin           string        `json:"origin"`
	Destination      string        `json:"destination"`
	OriginHubID      string        `json:"origin_hub_id"`
	DestinationHubID string        `json:"destination_hub_id"`
	RouteID          string        `json:"route_id"`
	VehicleID        string        `json:"vehicle_id"`
	Cargo            string        `json:"cargo"`
	CurrentCarrierID string        `json:"current_carrier_id"`
	DriverID         string        `json:"driver_id"`
	Status           string        `json:"status"`
	SLAHours         int           `json:"sla_hours"`
	ShipperPhone     string        `json:"shipper_phone"`
	Impact           *impactRecord `json:"impact,omitempty"`
}

type impactRecord struct {
	NoActionETAHours    float64 `json:"no_action_eta_hours"`
	PostActionETAHours  float64 `json:"post_action_eta_hours"`
	AvoidedPenaltyCents int64   `json:"avoided_penalty_cents"`
	ReassignDeltaCents  int64   `json:"reassign_delta_cents"`
	HandlingCostCents   int64   `json:"handling_cost_cents"`
}

type driverRecord struct {
	DriverID             string  `json:"driver_id"`
	Name                 string  `json:"name"`
	Phone                string  `json:"phone"`
	Plate                string  `json:"plate"`
	ContinuousDriveHours float64 `json:"continuous_drive_hours"`
	FatigueAlert         bool    `json:"fatigue_alert"`
}

type candidateRecord struct {
	WaybillID      string  `json:"waybill_id"`
	Priority       int     `json:"priority"`
	CarrierID      string  `json:"carrier_id"`
	Name           string  `json:"name"`
	ETAHours       int     `json:"eta_hours"`
	ReliabilityPct float64 `json:"reliability_pct"`
}

type trackingRecord struct {
	WaybillID   string   `json:"waybill_id"`
	Sequence    int      `json:"sequence"`
	Label       string   `json:"label"`
	RecordedAt  string   `json:"recorded_at"`
	Longitude   float64  `json:"longitude"`
	Latitude    float64  `json:"latitude"`
	SpeedKPH    int      `json:"speed_kph"`
	StopHours   *float64 `json:"stop_hours,omitempty"`
	Anomaly     bool     `json:"anomaly"`
	AnomalyType string   `json:"anomaly_type,omitempty"`
}

type weatherRecord struct {
	RouteID     string `json:"route_id"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Sequence    int    `json:"sequence"`
	Segment     string `json:"segment"`
	Condition   string `json:"condition"`
	AlertLevel  string `json:"alert_level"`
}

type routeCoordinate struct {
	Longitude float64
	Latitude  float64
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("datagenerate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", defaultOutputPath, "generated JSON data file")
	count := flags.Int("waybills", defaultWaybillCount, "number of waybills")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *output == "" || *count < len(hubSeeds) {
		fmt.Fprintf(
			stderr,
			"usage: datagenerate --output <file.json> --waybills <%d or more>\n",
			len(hubSeeds),
		)
		return 2
	}
	value := generate(*count)
	raw, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(stderr, "encode simulated data: %v\n", err)
		return 1
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fmt.Fprintln(stderr, "create output directory: failed")
		return 1
	}
	if err := os.WriteFile(*output, raw, 0o644); err != nil {
		fmt.Fprintln(stderr, "write output file: failed")
		return 1
	}
	fmt.Fprintf(
		stdout,
		"generated dataset=simulated-network-v1 hubs=%d routes=%d waybills=%d output=%s\n",
		len(value.Hubs),
		len(value.Routes),
		len(value.Waybills),
		filepath.Base(*output),
	)
	return 0
}

func generate(waybillCount int) dataset {
	result := dataset{
		SchemaVersion: "v1",
		DatasetID:     "simulated-network-v1",
	}
	for index, seed := range hubSeeds {
		result.Hubs = append(result.Hubs, hubRecord{
			HubID:         fmt.Sprintf("HUB-%03d", index+1),
			Name:          seed.City + "公路港",
			Province:      seed.Province,
			City:          seed.City,
			Longitude:     seed.Longitude,
			Latitude:      seed.Latitude,
			DailyCapacity: 1200 + (index*137)%2600,
		})
	}
	for index, origin := range result.Hubs {
		destination := result.Hubs[(index+17)%len(result.Hubs)]
		distance := haversineKM(
			origin.Longitude,
			origin.Latitude,
			destination.Longitude,
			destination.Latitude,
		)
		result.Routes = append(result.Routes, routeRecord{
			RouteID:          fmt.Sprintf("ROUTE-%03d", index+1),
			OriginHubID:      origin.HubID,
			DestinationHubID: destination.HubID,
			DistanceKM:       max(80, int(math.Round(distance*1.18))),
			StandardHours:    max(2, int(math.Ceil(distance/62))+2),
		})
		alert := "none"
		condition := "晴"
		if index%5 == 2 {
			alert = "orange"
			condition = "暴雨"
		}
		result.Weather = append(result.Weather, weatherRecord{
			RouteID:     fmt.Sprintf("ROUTE-%03d", index+1),
			Origin:      origin.City,
			Destination: destination.City,
			Sequence:    1,
			Segment:     origin.City + "至" + destination.City + "干线",
			Condition:   condition,
			AlertLevel:  alert,
		})
	}

	baseTime := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	anomalyTypes := []string{"delay", "damage", "loss", "weather", "fatigue"}
	cargoTypes := []string{"精密仪器", "汽车配件", "医药冷链", "工业设备", "消费电子"}
	vehicleTypes := []string{"厢式货车", "高栏货车", "冷链货车", "新能源牵引车"}
	for index := range waybillCount {
		number := index + 1
		routeIndex := index % len(result.Routes)
		route := result.Routes[routeIndex]
		origin := result.Hubs[routeIndex]
		destination := result.Hubs[(routeIndex+17)%len(result.Hubs)]
		waybillID := fmt.Sprintf("YD202610%04d", number)
		driverID := fmt.Sprintf("DRIVER-%04d", number)
		vehicleID := fmt.Sprintf("VEHICLE-%04d", number)
		anomalous := index%3 == 0
		anomalyType := ""
		status := "in_transit"
		if anomalous {
			anomalyType = anomalyTypes[routeIndex%len(anomalyTypes)]
			status = anomalyType
		}
		fatigue := anomalyType == "fatigue" || index%13 == 0
		continuousHours := 4.5 + float64(index%9)/2
		stopHours := 1.0 + float64(index%8)/2
		result.Vehicles = append(result.Vehicles, vehicleRecord{
			VehicleID:        vehicleID,
			MaskedPlate:      fmt.Sprintf("仿%c****%02d", 'A'+rune(index%24), number%100),
			Type:             vehicleTypes[index%len(vehicleTypes)],
			LoadCapacityTons: 8 + float64(index%7)*1.5,
		})
		result.Drivers = append(result.Drivers, driverRecord{
			DriverID:             driverID,
			Name:                 fmt.Sprintf("仿真司机%03d", number),
			Phone:                fmt.Sprintf("13988%06d", number),
			Plate:                fmt.Sprintf("仿%c%05d", 'A'+rune(index%24), number),
			ContinuousDriveHours: continuousHours,
			FatigueAlert:         fatigue,
		})
		result.Waybills = append(result.Waybills, waybillRecord{
			WaybillID:        waybillID,
			Origin:           origin.City,
			Destination:      destination.City,
			OriginHubID:      origin.HubID,
			DestinationHubID: destination.HubID,
			RouteID:          route.RouteID,
			VehicleID:        vehicleID,
			Cargo:            cargoTypes[index%len(cargoTypes)],
			CurrentCarrierID: fmt.Sprintf("CARRIER-%02d", index%12+1),
			DriverID:         driverID,
			Status:           status,
			SLAHours:         route.StandardHours + 6,
			ShipperPhone:     fmt.Sprintf("13877%06d", number),
			Impact: simulationImpact(
				anomalous,
				index,
				route.StandardHours,
				stopHours,
			),
		})
		for priority := 1; priority <= 2; priority++ {
			carrierNumber := (index + priority*7) % 24
			result.WaybillCandidates = append(
				result.WaybillCandidates,
				candidateRecord{
					WaybillID:      waybillID,
					Priority:       priority,
					CarrierID:      fmt.Sprintf("CARRIER-ALT-%02d", carrierNumber+1),
					Name:           fmt.Sprintf("仿真联运%02d", carrierNumber+1),
					ETAHours:       5 + (index+priority*3)%15,
					ReliabilityPct: 93.1 + float64((index+priority)%57)/10,
				},
			)
		}
		start := baseTime.Add(time.Duration(index) * 7 * time.Minute)
		result.Tracking = append(
			result.Tracking,
			buildTrackingRecords(
				waybillID,
				routeIndex,
				origin,
				destination,
				route,
				start,
				anomalous,
				anomalyType,
				stopHours,
			)...,
		)
	}
	return result
}

func buildTrackingRecords(
	waybillID string,
	routeIndex int,
	origin hubRecord,
	destination hubRecord,
	route routeRecord,
	start time.Time,
	anomalous bool,
	anomalyType string,
	stopHours float64,
) []trackingRecord {
	coordinates := buildRouteCoordinates(origin, destination, routeIndex)
	lastIndex := len(coordinates) - 1
	anomalyIndex := lastIndex / 2
	duration := time.Duration(route.StandardHours) * time.Hour
	records := make([]trackingRecord, 0, len(coordinates))
	for index, coordinate := range coordinates {
		isAnomaly := anomalous && index == anomalyIndex
		pointAnomalyType := ""
		if isAnomaly {
			pointAnomalyType = anomalyType
		}
		records = append(records, trackingRecord{
			WaybillID:   waybillID,
			Sequence:    index + 1,
			Label:       trackingPointLabel(origin, destination, index, lastIndex),
			RecordedAt:  start.Add(duration * time.Duration(index) / time.Duration(lastIndex)).Format(time.RFC3339),
			Longitude:   coordinate.Longitude,
			Latitude:    coordinate.Latitude,
			SpeedKPH:    trackingPointSpeed(routeIndex, index, lastIndex, isAnomaly),
			StopHours:   optionalStop(isAnomaly, stopHours),
			Anomaly:     isAnomaly,
			AnomalyType: pointAnomalyType,
		})
	}
	return records
}

func buildRouteCoordinates(
	origin hubRecord,
	destination hubRecord,
	routeIndex int,
) []routeCoordinate {
	middleLatitude := (origin.Latitude + destination.Latitude) / 2
	longitudeScale := math.Cos(middleLatitude * math.Pi / 180)
	scaledLongitudeDelta := (destination.Longitude - origin.Longitude) * longitudeScale
	latitudeDelta := destination.Latitude - origin.Latitude
	directDistance := math.Hypot(scaledLongitudeDelta, latitudeDelta)
	normalLongitude := -latitudeDelta / directDistance
	normalLatitude := scaledLongitudeDelta / directDistance
	direction := 1.0
	if routeIndex%2 != 0 {
		direction = -1
	}
	amplitude := math.Min(
		1.2,
		directDistance*(0.065+float64(routeIndex%5)*0.006),
	)

	coordinates := make([]routeCoordinate, 0, trackingPointCount)
	for index := range trackingPointCount {
		if index == 0 {
			coordinates = append(coordinates, routeCoordinate{
				Longitude: origin.Longitude,
				Latitude:  origin.Latitude,
			})
			continue
		}
		if index == trackingPointCount-1 {
			coordinates = append(coordinates, routeCoordinate{
				Longitude: destination.Longitude,
				Latitude:  destination.Latitude,
			})
			continue
		}
		progress := float64(index) / float64(trackingPointCount-1)
		lateralOffset := direction * amplitude *
			(math.Sin(math.Pi*progress) + 0.28*math.Sin(2*math.Pi*progress))
		coordinates = append(coordinates, routeCoordinate{
			Longitude: roundCoordinate(
				origin.Longitude +
					(scaledLongitudeDelta*progress+normalLongitude*lateralOffset)/
						longitudeScale,
			),
			Latitude: roundCoordinate(
				origin.Latitude +
					latitudeDelta*progress +
					normalLatitude*lateralOffset,
			),
		})
	}
	return coordinates
}

func trackingPointLabel(
	origin hubRecord,
	destination hubRecord,
	index int,
	lastIndex int,
) string {
	switch index {
	case 0:
		return origin.Name
	case lastIndex:
		return destination.Name
	case lastIndex / 2:
		return routeMidpointLabel(origin, destination)
	default:
		return fmt.Sprintf("%s至%s干线轨迹点 %d", origin.City, destination.City, index)
	}
}

func trackingPointSpeed(
	routeIndex int,
	index int,
	lastIndex int,
	anomalous bool,
) int {
	if index == 0 || index == lastIndex || anomalous {
		return 0
	}
	return 62 + (routeIndex+index*3)%11
}

func optionalStop(anomalous bool, value float64) *float64 {
	if !anomalous {
		return nil
	}
	return &value
}

func simulationImpact(
	anomalous bool,
	index int,
	standardHours int,
	stopHours float64,
) *impactRecord {
	if !anomalous {
		return nil
	}
	return &impactRecord{
		NoActionETAHours:    float64(standardHours) + 8 + stopHours,
		PostActionETAHours:  float64(standardHours) + 2 + float64(index%3)/2,
		AvoidedPenaltyCents: int64(120_000 + index%7*8_000),
		ReassignDeltaCents:  int64(18_000 + index%5*3_000),
		HandlingCostCents:   int64(5_000 + index%4*1_000),
	}
}

func routeMidpointLabel(origin, destination hubRecord) string {
	return origin.City + "至" + destination.City + "中途节点"
}

func roundCoordinate(value float64) float64 {
	return math.Round(value*10_000) / 10_000
}

func haversineKM(lon1, lat1, lon2, lat2 float64) float64 {
	const earthRadiusKM = 6371
	toRadians := math.Pi / 180
	latitudeDelta := (lat2 - lat1) * toRadians
	longitudeDelta := (lon2 - lon1) * toRadians
	left := math.Sin(latitudeDelta/2) * math.Sin(latitudeDelta/2)
	right := math.Cos(lat1*toRadians) *
		math.Cos(lat2*toRadians) *
		math.Sin(longitudeDelta/2) *
		math.Sin(longitudeDelta/2)
	return 2 * earthRadiusKM * math.Asin(math.Sqrt(left+right))
}

var hubSeeds = []hubSeed{
	{City: "北京", Province: "北京", Longitude: 116.4074, Latitude: 39.9042},
	{City: "天津", Province: "天津", Longitude: 117.2009, Latitude: 39.0842},
	{City: "石家庄", Province: "河北", Longitude: 114.5149, Latitude: 38.0428},
	{City: "唐山", Province: "河北", Longitude: 118.1802, Latitude: 39.6309},
	{City: "太原", Province: "山西", Longitude: 112.5489, Latitude: 37.8706},
	{City: "呼和浩特", Province: "内蒙古", Longitude: 111.7492, Latitude: 40.8426},
	{City: "沈阳", Province: "辽宁", Longitude: 123.4315, Latitude: 41.8057},
	{City: "大连", Province: "辽宁", Longitude: 121.6147, Latitude: 38.9140},
	{City: "长春", Province: "吉林", Longitude: 125.3235, Latitude: 43.8171},
	{City: "哈尔滨", Province: "黑龙江", Longitude: 126.5349, Latitude: 45.8038},
	{City: "上海", Province: "上海", Longitude: 121.4737, Latitude: 31.2304},
	{City: "南京", Province: "江苏", Longitude: 118.7969, Latitude: 32.0603},
	{City: "苏州", Province: "江苏", Longitude: 120.5853, Latitude: 31.2989},
	{City: "无锡", Province: "江苏", Longitude: 120.3119, Latitude: 31.4912},
	{City: "常州", Province: "江苏", Longitude: 119.9741, Latitude: 31.8112},
	{City: "南通", Province: "江苏", Longitude: 120.8943, Latitude: 31.9802},
	{City: "徐州", Province: "江苏", Longitude: 117.2841, Latitude: 34.2058},
	{City: "连云港", Province: "江苏", Longitude: 119.2216, Latitude: 34.5967},
	{City: "合肥", Province: "安徽", Longitude: 117.2272, Latitude: 31.8206},
	{City: "芜湖", Province: "安徽", Longitude: 118.3765, Latitude: 31.3263},
	{City: "福州", Province: "福建", Longitude: 119.2965, Latitude: 26.0745},
	{City: "厦门", Province: "福建", Longitude: 118.0894, Latitude: 24.4798},
	{City: "泉州", Province: "福建", Longitude: 118.6757, Latitude: 24.8741},
	{City: "南昌", Province: "江西", Longitude: 115.8582, Latitude: 28.6829},
	{City: "济南", Province: "山东", Longitude: 117.1205, Latitude: 36.6512},
	{City: "青岛", Province: "山东", Longitude: 120.3826, Latitude: 36.0671},
	{City: "临沂", Province: "山东", Longitude: 118.3564, Latitude: 35.1047},
	{City: "郑州", Province: "河南", Longitude: 113.6254, Latitude: 34.7466},
	{City: "洛阳", Province: "河南", Longitude: 112.4540, Latitude: 34.6197},
	{City: "武汉", Province: "湖北", Longitude: 114.3054, Latitude: 30.5931},
	{City: "长沙", Province: "湖南", Longitude: 112.9388, Latitude: 28.2282},
	{City: "广州", Province: "广东", Longitude: 113.2644, Latitude: 23.1291},
	{City: "深圳", Province: "广东", Longitude: 114.0579, Latitude: 22.5431},
	{City: "佛山", Province: "广东", Longitude: 113.1214, Latitude: 23.0215},
	{City: "东莞", Province: "广东", Longitude: 113.7518, Latitude: 23.0207},
	{City: "南宁", Province: "广西", Longitude: 108.3669, Latitude: 22.8170},
	{City: "海口", Province: "海南", Longitude: 110.1983, Latitude: 20.0440},
	{City: "重庆", Province: "重庆", Longitude: 106.5516, Latitude: 29.5630},
	{City: "成都", Province: "四川", Longitude: 104.0665, Latitude: 30.5723},
	{City: "绵阳", Province: "四川", Longitude: 104.6796, Latitude: 31.4675},
	{City: "贵阳", Province: "贵州", Longitude: 106.6302, Latitude: 26.6477},
	{City: "昆明", Province: "云南", Longitude: 102.8329, Latitude: 24.8801},
	{City: "拉萨", Province: "西藏", Longitude: 91.1409, Latitude: 29.6456},
	{City: "西安", Province: "陕西", Longitude: 108.9398, Latitude: 34.3416},
	{City: "兰州", Province: "甘肃", Longitude: 103.8343, Latitude: 36.0611},
	{City: "西宁", Province: "青海", Longitude: 101.7782, Latitude: 36.6171},
	{City: "银川", Province: "宁夏", Longitude: 106.2309, Latitude: 38.4872},
	{City: "乌鲁木齐", Province: "新疆", Longitude: 87.6168, Latitude: 43.8256},
	{City: "喀什", Province: "新疆", Longitude: 75.9898, Latitude: 39.4704},
	{City: "杭州", Province: "浙江", Longitude: 120.1551, Latitude: 30.2741},
	{City: "宁波", Province: "浙江", Longitude: 121.5503, Latitude: 29.8746},
	{City: "温州", Province: "浙江", Longitude: 120.6994, Latitude: 27.9943},
	{City: "金华", Province: "浙江", Longitude: 119.6474, Latitude: 29.0792},
	{City: "嘉兴", Province: "浙江", Longitude: 120.7555, Latitude: 30.7461},
	{City: "绍兴", Province: "浙江", Longitude: 120.5802, Latitude: 30.0303},
	{City: "台州", Province: "浙江", Longitude: 121.4208, Latitude: 28.6564},
	{City: "烟台", Province: "山东", Longitude: 121.4479, Latitude: 37.4638},
	{City: "潍坊", Province: "山东", Longitude: 119.1618, Latitude: 36.7069},
	{City: "淄博", Province: "山东", Longitude: 118.0549, Latitude: 36.8135},
	{City: "济宁", Province: "山东", Longitude: 116.5872, Latitude: 35.4149},
	{City: "南阳", Province: "河南", Longitude: 112.5283, Latitude: 32.9908},
	{City: "襄阳", Province: "湖北", Longitude: 112.1224, Latitude: 32.0090},
	{City: "宜昌", Province: "湖北", Longitude: 111.2865, Latitude: 30.6919},
	{City: "岳阳", Province: "湖南", Longitude: 113.1289, Latitude: 29.3571},
	{City: "株洲", Province: "湖南", Longitude: 113.1340, Latitude: 27.8274},
	{City: "衡阳", Province: "湖南", Longitude: 112.5719, Latitude: 26.8934},
	{City: "桂林", Province: "广西", Longitude: 110.2900, Latitude: 25.2736},
	{City: "柳州", Province: "广西", Longitude: 109.4281, Latitude: 24.3264},
	{City: "德阳", Province: "四川", Longitude: 104.3979, Latitude: 31.1269},
	{City: "南充", Province: "四川", Longitude: 106.1107, Latitude: 30.8373},
	{City: "宝鸡", Province: "陕西", Longitude: 107.2379, Latitude: 34.3619},
	{City: "咸阳", Province: "陕西", Longitude: 108.7088, Latitude: 34.3296},
}
