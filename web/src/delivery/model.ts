import type {
  DeliveryLoadStage,
  DeliveryLocation,
  DeliveryTask,
  DeliveryTrip,
  DeliveryVehicle,
  DeliveryWorkspace,
} from "./contract";

export type DeliverySelection = {
  dutyIndex: number;
  tripIndex: number;
  stopIndex: number;
};

export type DeliveryStep = DeliverySelection & {
  key: string;
  sequence: number;
  vehicleID: string;
  driverIDs: readonly string[];
  tripID: string;
  location: DeliveryLocation;
  tasks: readonly DeliveryTask[];
  arrivalAt: string;
  serviceAt: string;
  departureAt: string;
  loadStage: DeliveryLoadStage | null;
  socWh: number | null;
};

export type DeliveryTripContext = {
  trip: DeliveryTrip;
  vehicle: DeliveryVehicle;
  driverIDs: readonly string[];
};

export type RoutePlotPoint = {
  key: string;
  sequence: number;
  xPercent: number;
  yPercent: number;
  selected: boolean;
  kind: DeliveryLocation["kind"];
};

export function buildDeliverySteps(
  workspace: DeliveryWorkspace,
): readonly DeliveryStep[] {
  const locations = new Map(
    workspace.problem.locations.map((location) => [location.id, location]),
  );
  const tasks = new Map(
    workspace.problem.tasks.map((task) => [task.id, task]),
  );
  const steps: DeliveryStep[] = [];

  workspace.plan.duties.forEach((duty, dutyIndex) => {
    duty.trips.forEach((trip, tripIndex) => {
      trip.stops.forEach((stop, stopIndex) => {
        const location = locations.get(stop.location_id);
        if (location === undefined) {
          return;
        }
        const stopTasks = stop.task_ids.flatMap((taskID) => {
          const task = tasks.get(taskID);
          return task === undefined ? [] : [task];
        });
        const loadStage =
          trip.load_stages.find(
            (stage) => stage.after_stop_index === stopIndex,
          ) ?? null;
        const energyLeg = trip.energy.find(
          (leg) => leg.to_stop_index === stopIndex,
        );
        steps.push({
          dutyIndex,
          tripIndex,
          stopIndex,
          key: `${duty.vehicle_id}/${trip.id}/${stopIndex}`,
          sequence: steps.length + 1,
          vehicleID: duty.vehicle_id,
          driverIDs: duty.driver_ids,
          tripID: trip.id,
          location,
          tasks: stopTasks,
          arrivalAt: stop.arrival_at,
          serviceAt: stop.service_at,
          departureAt: stop.departure_at,
          loadStage,
          socWh: energyLeg?.end_soc_wh ?? null,
        });
      });
    });
  });

  return steps;
}

export function initialDeliverySelection(
  workspace: DeliveryWorkspace,
): DeliverySelection {
  const firstStep = buildDeliverySteps(workspace)[0];
  return firstStep === undefined
    ? { dutyIndex: 0, tripIndex: 0, stopIndex: 0 }
    : {
        dutyIndex: firstStep.dutyIndex,
        tripIndex: firstStep.tripIndex,
        stopIndex: firstStep.stopIndex,
      };
}

export function findStepIndex(
  steps: readonly DeliveryStep[],
  selection: DeliverySelection,
): number {
  return steps.findIndex(
    (step) =>
      step.dutyIndex === selection.dutyIndex &&
      step.tripIndex === selection.tripIndex &&
      step.stopIndex === selection.stopIndex,
  );
}

export function selectionForStep(
  step: DeliveryStep,
): DeliverySelection {
  return {
    dutyIndex: step.dutyIndex,
    tripIndex: step.tripIndex,
    stopIndex: step.stopIndex,
  };
}

export function resolveTripContext(
  workspace: DeliveryWorkspace,
  selection: DeliverySelection,
): DeliveryTripContext | null {
  const duty = workspace.plan.duties[selection.dutyIndex];
  const trip = duty?.trips[selection.tripIndex];
  if (duty === undefined || trip === undefined) {
    return null;
  }
  const vehicle = workspace.problem.vehicles.find(
    (candidate) => candidate.id === duty.vehicle_id,
  );
  if (vehicle === undefined) {
    return null;
  }
  return { trip, vehicle, driverIDs: duty.driver_ids };
}

export function buildRoutePlot(
  steps: readonly DeliveryStep[],
  selection: DeliverySelection,
): readonly RoutePlotPoint[] {
  if (steps.length === 0) {
    return [];
  }
  const longitudes = steps.map(
    (step) => step.location.longitude_microdegrees,
  );
  const latitudes = steps.map((step) => step.location.latitude_microdegrees);
  const minLongitude = Math.min(...longitudes);
  const maxLongitude = Math.max(...longitudes);
  const minLatitude = Math.min(...latitudes);
  const maxLatitude = Math.max(...latitudes);
  const longitudeSpan = Math.max(maxLongitude - minLongitude, 1);
  const latitudeSpan = Math.max(maxLatitude - minLatitude, 1);

  return steps.map((step) => ({
    key: step.key,
    sequence: step.sequence,
    xPercent:
      8 +
      ((step.location.longitude_microdegrees - minLongitude) /
        longitudeSpan) *
        84,
    yPercent:
      92 -
      ((step.location.latitude_microdegrees - minLatitude) / latitudeSpan) *
        84,
    selected:
      step.dutyIndex === selection.dutyIndex &&
      step.tripIndex === selection.tripIndex &&
      step.stopIndex === selection.stopIndex,
    kind: step.location.kind,
  }));
}

export function taskKindLabel(kind: DeliveryTask["kind"]): string {
  switch (kind) {
    case "pickup":
      return "提货";
    case "delivery":
      return "配送";
    case "service":
      return "服务";
    case "depot_load":
      return "仓内装车";
    case "depot_unload":
      return "回仓卸载";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

export function locationKindLabel(
  kind: DeliveryLocation["kind"],
): string {
  switch (kind) {
    case "customer":
      return "客户";
    case "depot":
      return "仓库";
    case "charger":
      return "充电站";
    case "waypoint":
      return "途经点";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

export function segmentKindLabel(
  kind: DeliveryTrip["schedule"][number]["kind"],
): string {
  switch (kind) {
    case "drive":
      return "驾驶";
    case "service":
      return "服务";
    case "wait":
      return "等待";
    case "break":
      return "休息";
    case "charge":
      return "充电";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

export function runStateLabel(
  state: DeliveryWorkspace["run_state"],
): string {
  switch (state) {
    case "queued":
      return "排队中";
    case "solving":
      return "求解中";
    case "validating":
      return "校验中";
    case "candidate":
      return "候选计划";
    case "awaiting_approval":
      return "等待审批";
    case "executing":
      return "执行中";
    case "completed":
      return "已完成";
    case "failed":
      return "求解失败";
    case "manual_review":
      return "人工复核";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

export function formatClock(timestamp: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone: "Asia/Shanghai",
  }).format(new Date(timestamp));
}

export function formatDateTime(timestamp: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone: "Asia/Shanghai",
  }).format(new Date(timestamp));
}

export function formatDuration(seconds: number): string {
  const hours = Math.floor(seconds / 3_600);
  const minutes = Math.round((seconds % 3_600) / 60);
  if (hours === 0) {
    return `${minutes} 分钟`;
  }
  return `${hours} 小时 ${minutes} 分钟`;
}

export function formatDistance(meters: number): string {
  return `${(meters / 1_000).toFixed(1)} km`;
}

export function formatCurrency(cents: number): string {
  return new Intl.NumberFormat("zh-CN", {
    style: "currency",
    currency: "CNY",
    maximumFractionDigits: 0,
  }).format(cents / 100);
}

export function formatPercentFromPPM(partsPerMillion: number): string {
  return `${(partsPerMillion / 10_000).toFixed(1)}%`;
}
