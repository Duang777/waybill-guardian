import type { HubOverview, RouteOverview } from "../api";

export type FacilityLayoutKind =
  | "linear"
  | "cross-dock"
  | "courtyard"
  | "split-yard"
  | "gateway";

export type FacilityElementKind =
  | "ground"
  | "perimeter"
  | "road"
  | "apron"
  | "warehouse"
  | "roof"
  | "dock"
  | "slot"
  | "cargo"
  | "marking"
  | "gatehouse"
  | "tower"
  | "beacon";

export type FacilityElement = {
  kind: FacilityElementKind;
  x: number;
  centerY: number;
  z: number;
  width: number;
  height: number;
  depth: number;
  rotationY: number;
};

export type FacilityTransportRouteKind =
  | "gate-to-dock"
  | "dock-to-yard"
  | "yard-to-gate";

export type FacilityTransportRouteStatus = "active" | "risk";

export type FacilityRoutePoint = readonly [x: number, z: number];

export type FacilityTransportRoute = {
  kind: FacilityTransportRouteKind;
  status: FacilityTransportRouteStatus;
  points: readonly [
    FacilityRoutePoint,
    FacilityRoutePoint,
    ...FacilityRoutePoint[],
  ];
};

export type FacilityTransportSegment = {
  id: string;
  status: FacilityTransportRouteStatus;
  routeKinds: readonly FacilityTransportRouteKind[];
  from: FacilityRoutePoint;
  to: FacilityRoutePoint;
};

export type FacilityVehicleState = "moving" | "loading" | "alert";

export type FacilityVehiclePath = {
  points: readonly [
    FacilityRoutePoint,
    FacilityRoutePoint,
    ...FacilityRoutePoint[],
  ];
  dockPhase: number;
};

export type FacilityVehicle = {
  id: string;
  state: FacilityVehicleState;
  phase: number;
  speed: number;
};

export type FacilityRouteSample = {
  x: number;
  z: number;
  rotationY: number;
};

export type FacilityLayout = {
  kind: FacilityLayoutKind;
  label: string;
  signature: string;
  campusWidth: number;
  campusDepth: number;
  warehouseCount: number;
  dockBays: number;
  storageSlots: number;
  occupiedSlots: number;
  elements: readonly FacilityElement[];
  transportRoutes: readonly FacilityTransportRoute[];
  transportSegments: readonly FacilityTransportSegment[];
  vehiclePath: FacilityVehiclePath;
  vehicles: readonly FacilityVehicle[];
};

type LayoutContext = {
  minimumCapacity: number;
  maximumCapacity: number;
  maximumInFlight: number;
  maximumWaybills: number;
  maximumAnomalies: number;
  minimumRouteDistance: number;
  maximumRouteDistance: number;
};

type LayoutSignals = {
  capacity: number;
  activity: number;
  throughput: number;
  risk: number;
  routeDistance: number;
  longitude: number;
  latitude: number;
};

type PrimaryRoute = {
  route: RouteOverview;
  entranceSide: -1 | 1;
};

type DockLine = {
  axis: "x" | "z";
  x: number;
  z: number;
  span: number;
  apronDepth: number;
  facing: -1 | 1;
};

type YardGrid = {
  x: number;
  z: number;
  columns: number;
  xStep: number;
  zStep: number;
  rotationY: number;
};

type FacilityRoadSpine =
  | { axis: "x"; z: number }
  | { axis: "z"; x: number };

type FacilityTopology = {
  dockLine: DockLine;
  yard: YardGrid;
  spine: FacilityRoadSpine;
};

const layoutLabels = {
  linear: "长廊干线型",
  "cross-dock": "十字转运型",
  courtyard: "围合仓配型",
  "split-yard": "双场站型",
  gateway: "门户枢纽型",
} satisfies Record<FacilityLayoutKind, string>;

export function buildFacilityLayouts(
  hubs: readonly HubOverview[],
  routes: readonly RouteOverview[],
): ReadonlyMap<string, FacilityLayout> {
  const context = buildLayoutContext(hubs, routes);
  return new Map(
    hubs.map((hub) => {
      const primaryRoute = findPrimaryRoute(hub.hub_id, routes);
      return [
        hub.hub_id,
        buildFacilityLayout(hub, primaryRoute, context),
      ];
    }),
  );
}

function buildLayoutContext(
  hubs: readonly HubOverview[],
  routes: readonly RouteOverview[],
): LayoutContext {
  const capacities = hubs.map((hub) => hub.daily_capacity);
  const distances = routes.map((route) => route.distance_km);
  return {
    minimumCapacity: minimum(capacities, 0),
    maximumCapacity: maximum(capacities, 1),
    maximumInFlight: maximum(hubs.map((hub) => hub.in_flight), 1),
    maximumWaybills: maximum(hubs.map((hub) => hub.waybills), 1),
    maximumAnomalies: maximum(hubs.map((hub) => hub.anomalies), 1),
    minimumRouteDistance: minimum(distances, 0),
    maximumRouteDistance: maximum(distances, 1),
  };
}

function buildFacilityLayout(
  hub: HubOverview,
  primaryRoute: PrimaryRoute | undefined,
  context: LayoutContext,
): FacilityLayout {
  const signals = buildLayoutSignals(hub, primaryRoute, context);
  const kind = chooseLayoutKind(signals);
  const campusWidth =
    1.18 + signals.capacity * 0.48 + signals.throughput * 0.1;
  const campusDepth =
    0.86 + signals.routeDistance * 0.3 + signals.activity * 0.08;
  const dockBays =
    4 + Math.round(signals.capacity * 4) + Math.min(hub.waybills, 2);
  const storageSlots =
    6 + Math.round(signals.capacity * 7) + Math.min(hub.in_flight, 3);
  const occupiedSlots =
    hub.in_flight === 0
      ? 0
      : Math.max(1, Math.ceil(signals.activity * storageSlots));
  const elements: FacilityElement[] = [];

  addCampus(elements, campusWidth, campusDepth);
  const topology = addTopology({
    elements,
    kind,
    campusWidth,
    campusDepth,
    signals,
    warehouseTarget: 1 + Math.round(signals.capacity * 2),
  });
  addDockLine(elements, topology.dockLine, dockBays);
  addStorageYard(
    elements,
    topology.yard,
    storageSlots,
    occupiedSlots,
    signals.activity,
  );
  const entrance = addEntrance({
    elements,
    campusWidth,
    campusDepth,
    entranceSide: primaryRoute?.entranceSide ?? 1,
    signals,
  });
  const transportRoutes = buildTransportRoutes({
    entrance,
    topology,
    storageSlots,
    hubRisk: hub.anomalies > 0,
    routeRisk: (primaryRoute?.route.anomalies ?? 0) > 0,
  });
  const transportSegments = buildFacilityTransportSegments(transportRoutes);
  const vehiclePath = buildFacilityVehiclePath(transportRoutes);
  const vehicles = buildFacilityVehicles({
    hub,
    primaryRoute,
    signals,
    vehiclePath,
  });

  const warehouseCount = elements.filter(
    (element) => element.kind === "warehouse",
  ).length;
  const elementSignature = elements
    .map((element) => [
      element.kind,
      roundSignature(element.x),
      roundSignature(element.z),
      roundSignature(element.width),
      roundSignature(element.depth),
      roundSignature(element.height),
      roundSignature(element.rotationY),
    ].join(","))
    .join("|");
  const routeSignature = transportRoutes
    .map((route) => [
      route.kind,
      route.status,
      route.points
        .map(([x, z]) => `${roundSignature(x)},${roundSignature(z)}`)
        .join(";"),
    ].join(","))
    .join("|");
  const vehicleSignature = vehicles
    .map((vehicle) => [
      vehicle.id,
      vehicle.state,
      roundSignature(vehicle.phase),
      roundSignature(vehicle.speed),
    ].join(","))
    .join("|");
  const signature =
    `${elementSignature}#${routeSignature}#${vehicleSignature}`;

  return {
    kind,
    label: layoutLabels[kind],
    signature,
    campusWidth,
    campusDepth,
    warehouseCount,
    dockBays,
    storageSlots,
    occupiedSlots,
    elements,
    transportRoutes,
    transportSegments,
    vehiclePath,
    vehicles,
  };
}

function buildLayoutSignals(
  hub: HubOverview,
  primaryRoute: PrimaryRoute | undefined,
  context: LayoutContext,
): LayoutSignals {
  return {
    capacity: normalize(
      hub.daily_capacity,
      context.minimumCapacity,
      context.maximumCapacity,
    ),
    activity: Math.min(hub.in_flight / context.maximumInFlight, 1),
    throughput: Math.min(hub.waybills / context.maximumWaybills, 1),
    risk: Math.min(hub.anomalies / context.maximumAnomalies, 1),
    routeDistance: normalize(
      primaryRoute?.route.distance_km ?? context.minimumRouteDistance,
      context.minimumRouteDistance,
      context.maximumRouteDistance,
    ),
    longitude: normalize(hub.longitude, 73, 135),
    latitude: normalize(hub.latitude, 18, 54),
  };
}

function chooseLayoutKind(signals: LayoutSignals): FacilityLayoutKind {
  if (signals.routeDistance >= 0.58) {
    return "linear";
  }
  if (signals.capacity >= 0.72) {
    return "gateway";
  }
  if (signals.risk >= 0.85) {
    return "split-yard";
  }
  if (signals.activity >= 0.65) {
    return "cross-dock";
  }
  return "courtyard";
}

function addCampus(
  elements: FacilityElement[],
  campusWidth: number,
  campusDepth: number,
) {
  elements.push(
    facilityElement("ground", 0, -0.2, 0, campusWidth, 0.04, campusDepth),
    facilityElement(
      "perimeter",
      0,
      -0.172,
      -campusDepth / 2,
      campusWidth,
      0.014,
      0.018,
    ),
    facilityElement(
      "perimeter",
      0,
      -0.172,
      campusDepth / 2,
      campusWidth,
      0.014,
      0.018,
    ),
    facilityElement(
      "perimeter",
      -campusWidth / 2,
      -0.172,
      0,
      0.018,
      0.014,
      campusDepth,
    ),
    facilityElement(
      "perimeter",
      campusWidth / 2,
      -0.172,
      0,
      0.018,
      0.014,
      campusDepth,
    ),
  );
}

function addTopology({
  elements,
  kind,
  campusWidth,
  campusDepth,
  signals,
  warehouseTarget,
}: {
  elements: FacilityElement[];
  kind: FacilityLayoutKind;
  campusWidth: number;
  campusDepth: number;
  signals: LayoutSignals;
  warehouseTarget: number;
}): FacilityTopology {
  const warehouseHeight = 0.16 + signals.capacity * 0.08;
  const locationSkew = (signals.latitude - 0.5) * 0.16;
  switch (kind) {
    case "linear": {
      const warehouseWidth = campusWidth * 0.48;
      const warehouseDepth = 0.16 + signals.throughput * 0.07;
      const count = Math.max(2, warehouseTarget);
      for (let index = 0; index < count; index += 1) {
        const z = distributedPosition(
          -campusDepth * 0.26,
          campusDepth * 0.2,
          index,
          count,
        );
        addWarehouse(
          elements,
          -campusWidth * 0.12,
          z,
          warehouseWidth,
          warehouseDepth,
          warehouseHeight - index * 0.012,
          locationSkew,
        );
      }
      addRoad(
        elements,
        campusWidth * 0.33,
        0,
        campusWidth * 0.16,
        campusDepth * 0.82,
      );
      addRoadMarkings(elements, "z", campusWidth * 0.33, 0, campusDepth * 0.62);
      return {
        dockLine: {
          axis: "x",
          x: -campusWidth * 0.12,
          z: campusDepth * 0.31,
          span: warehouseWidth,
          apronDepth: campusDepth * 0.17,
          facing: 1,
        },
        yard: {
          x: -campusWidth * 0.39,
          z: -campusDepth * 0.28,
          columns: 3,
          xStep: 0.1,
          zStep: 0.1,
          rotationY: locationSkew,
        },
        spine: {
          axis: "z",
          x: campusWidth * 0.33,
        },
      };
    }
    case "cross-dock": {
      addWarehouse(
        elements,
        -campusWidth * 0.08,
        -campusDepth * 0.05,
        campusWidth * 0.62,
        0.2,
        warehouseHeight,
        0,
      );
      addWarehouse(
        elements,
        -campusWidth * 0.18,
        -campusDepth * 0.08,
        0.19,
        campusDepth * 0.54,
        warehouseHeight * 0.92,
        0,
      );
      if (warehouseTarget >= 3) {
        addWarehouse(
          elements,
          campusWidth * 0.2,
          -campusDepth * 0.26,
          campusWidth * 0.2,
          0.15,
          warehouseHeight * 0.78,
          -locationSkew,
        );
      }
      addRoad(
        elements,
        campusWidth * 0.33,
        0,
        campusWidth * 0.15,
        campusDepth * 0.82,
      );
      addRoad(
        elements,
        0,
        campusDepth * 0.31,
        campusWidth * 0.72,
        campusDepth * 0.13,
      );
      addRoadMarkings(elements, "z", campusWidth * 0.33, 0, campusDepth * 0.6);
      return {
        dockLine: {
          axis: "x",
          x: -campusWidth * 0.08,
          z: campusDepth * 0.12,
          span: campusWidth * 0.56,
          apronDepth: campusDepth * 0.17,
          facing: 1,
        },
        yard: {
          x: -campusWidth * 0.41,
          z: campusDepth * 0.25,
          columns: 4,
          xStep: 0.095,
          zStep: -0.1,
          rotationY: 0,
        },
        spine: {
          axis: "z",
          x: campusWidth * 0.33,
        },
      };
    }
    case "courtyard": {
      const sideDepth = campusDepth * 0.5;
      addWarehouse(
        elements,
        -campusWidth * 0.27,
        -campusDepth * 0.08,
        0.18,
        sideDepth,
        warehouseHeight,
        locationSkew * 0.5,
      );
      addWarehouse(
        elements,
        campusWidth * 0.12,
        -campusDepth * 0.08,
        0.18,
        sideDepth,
        warehouseHeight * 0.92,
        -locationSkew * 0.5,
      );
      if (warehouseTarget >= 2) {
        addWarehouse(
          elements,
          -campusWidth * 0.08,
          -campusDepth * 0.31,
          campusWidth * 0.52,
          0.16,
          warehouseHeight * 0.84,
          0,
        );
      }
      addRoad(
        elements,
        0,
        campusDepth * 0.34,
        campusWidth * 0.78,
        campusDepth * 0.14,
      );
      addRoadMarkings(elements, "x", 0, campusDepth * 0.34, campusWidth * 0.6);
      return {
        dockLine: {
          axis: "x",
          x: -campusWidth * 0.08,
          z: campusDepth * 0.18,
          span: campusWidth * 0.48,
          apronDepth: campusDepth * 0.17,
          facing: 1,
        },
        yard: {
          x: -campusWidth * 0.05,
          z: -campusDepth * 0.03,
          columns: 4,
          xStep: 0.1,
          zStep: 0.1,
          rotationY: 0,
        },
        spine: {
          axis: "x",
          z: campusDepth * 0.34,
        },
      };
    }
    case "split-yard": {
      addWarehouse(
        elements,
        -campusWidth * 0.27,
        -campusDepth * 0.11,
        campusWidth * 0.3,
        campusDepth * 0.34,
        warehouseHeight,
        locationSkew,
      );
      addWarehouse(
        elements,
        campusWidth * 0.25,
        campusDepth * 0.12,
        campusWidth * 0.28,
        campusDepth * 0.3,
        warehouseHeight * 0.88,
        -locationSkew,
      );
      if (warehouseTarget >= 3) {
        addWarehouse(
          elements,
          campusWidth * 0.24,
          -campusDepth * 0.27,
          campusWidth * 0.25,
          0.14,
          warehouseHeight * 0.72,
          0,
        );
      }
      addRoad(
        elements,
        0,
        0,
        campusWidth * 0.16,
        campusDepth * 0.88,
      );
      addRoadMarkings(elements, "z", 0, 0, campusDepth * 0.65);
      return {
        dockLine: {
          axis: "z",
          x: -campusWidth * 0.08,
          z: -campusDepth * 0.1,
          span: campusDepth * 0.45,
          apronDepth: campusWidth * 0.14,
          facing: 1,
        },
        yard: {
          x: campusWidth * 0.28,
          z: -campusDepth * 0.2,
          columns: 4,
          xStep: 0.1,
          zStep: 0.1,
          rotationY: -locationSkew,
        },
        spine: {
          axis: "z",
          x: 0,
        },
      };
    }
    case "gateway": {
      const angle = 0.12 + signals.longitude * 0.08;
      addWarehouse(
        elements,
        -campusWidth * 0.2,
        -campusDepth * 0.12,
        campusWidth * 0.42,
        0.2,
        warehouseHeight,
        angle,
      );
      addWarehouse(
        elements,
        campusWidth * 0.13,
        campusDepth * 0.11,
        campusWidth * 0.34,
        0.18,
        warehouseHeight * 0.9,
        -angle,
      );
      if (warehouseTarget >= 3) {
        addWarehouse(
          elements,
          -campusWidth * 0.2,
          campusDepth * 0.24,
          campusWidth * 0.24,
          0.14,
          warehouseHeight * 0.72,
          angle,
        );
      }
      addRoad(
        elements,
        campusWidth * 0.34,
        0,
        campusWidth * 0.14,
        campusDepth * 0.84,
      );
      addRoad(
        elements,
        0,
        campusDepth * 0.35,
        campusWidth * 0.72,
        campusDepth * 0.12,
      );
      addRoadMarkings(elements, "z", campusWidth * 0.34, 0, campusDepth * 0.62);
      return {
        dockLine: {
          axis: "x",
          x: -campusWidth * 0.12,
          z: campusDepth * 0.2,
          span: campusWidth * 0.48,
          apronDepth: campusDepth * 0.16,
          facing: 1,
        },
        yard: {
          x: -campusWidth * 0.41,
          z: -campusDepth * 0.26,
          columns: 3,
          xStep: 0.1,
          zStep: 0.1,
          rotationY: angle,
        },
        spine: {
          axis: "z",
          x: campusWidth * 0.34,
        },
      };
    }
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function addWarehouse(
  elements: FacilityElement[],
  x: number,
  z: number,
  width: number,
  depth: number,
  height: number,
  rotationY: number,
) {
  elements.push(
    facilityElement(
      "warehouse",
      x,
      -0.18 + height / 2,
      z,
      width,
      height,
      depth,
      rotationY,
    ),
    facilityElement(
      "roof",
      x,
      -0.18 + height + 0.012,
      z,
      width + 0.025,
      0.024,
      depth + 0.025,
      rotationY,
    ),
  );
}

function addRoad(
  elements: FacilityElement[],
  x: number,
  z: number,
  width: number,
  depth: number,
) {
  elements.push(
    facilityElement("road", x, -0.171, z, width, 0.014, depth),
  );
}

function addRoadMarkings(
  elements: FacilityElement[],
  axis: "x" | "z",
  x: number,
  z: number,
  span: number,
) {
  const count = 7;
  for (let index = 0; index < count; index += 1) {
    const position = distributedPosition(-span / 2, span / 2, index, count);
    elements.push(
      axis === "x"
        ? facilityElement(
            "marking",
            x + position,
            -0.16,
            z,
            0.055,
            0.008,
            0.016,
          )
        : facilityElement(
            "marking",
            x,
            -0.16,
            z + position,
            0.016,
            0.008,
            0.055,
          ),
    );
  }
}

function addDockLine(
  elements: FacilityElement[],
  dockLine: DockLine,
  dockBays: number,
) {
  const apronWidth =
    dockLine.axis === "x" ? dockLine.span : dockLine.apronDepth;
  const apronDepth =
    dockLine.axis === "x" ? dockLine.apronDepth : dockLine.span;
  elements.push(
    facilityElement(
      "apron",
      dockLine.x,
      -0.169,
      dockLine.z,
      apronWidth,
      0.018,
      apronDepth,
    ),
  );
  for (let index = 0; index < dockBays; index += 1) {
    const position = distributedPosition(
      -dockLine.span * 0.44,
      dockLine.span * 0.44,
      index,
      dockBays,
    );
    if (dockLine.axis === "x") {
      elements.push(
        facilityElement(
          "dock",
          dockLine.x + position,
          -0.12,
          dockLine.z - dockLine.facing * dockLine.apronDepth * 0.32,
          0.05,
          0.12,
          0.025,
        ),
        facilityElement(
          "marking",
          dockLine.x + position,
          -0.158,
          dockLine.z + dockLine.facing * dockLine.apronDepth * 0.1,
          0.012,
          0.008,
          dockLine.apronDepth * 0.72,
        ),
      );
    } else {
      elements.push(
        facilityElement(
          "dock",
          dockLine.x - dockLine.facing * dockLine.apronDepth * 0.32,
          -0.12,
          dockLine.z + position,
          0.025,
          0.12,
          0.05,
        ),
        facilityElement(
          "marking",
          dockLine.x + dockLine.facing * dockLine.apronDepth * 0.1,
          -0.158,
          dockLine.z + position,
          dockLine.apronDepth * 0.72,
          0.008,
          0.012,
        ),
      );
    }
  }
}

function addStorageYard(
  elements: FacilityElement[],
  yard: YardGrid,
  storageSlots: number,
  occupiedSlots: number,
  activity: number,
) {
  for (let index = 0; index < storageSlots; index += 1) {
    const column = index % yard.columns;
    const row = Math.floor(index / yard.columns);
    const x = yard.x + column * yard.xStep;
    const z = yard.z + row * yard.zStep;
    elements.push(
      facilityElement(
        "slot",
        x,
        -0.16,
        z,
        0.078,
        0.012,
        0.07,
        yard.rotationY,
      ),
    );
    if (index < occupiedSlots) {
      elements.push(
        facilityElement(
          "cargo",
          x,
          -0.13,
          z,
          0.062,
          0.05,
          0.055,
          yard.rotationY,
        ),
      );
      if (activity > 0.7 && index % 2 === 0) {
        elements.push(
          facilityElement(
            "cargo",
            x,
            -0.078,
            z,
            0.058,
            0.05,
            0.051,
            yard.rotationY,
          ),
        );
      }
    }
  }
}

function addEntrance({
  elements,
  campusWidth,
  campusDepth,
  entranceSide,
  signals,
}: {
  elements: FacilityElement[];
  campusWidth: number;
  campusDepth: number;
  entranceSide: -1 | 1;
  signals: LayoutSignals;
}): FacilityRoutePoint {
  const entranceX = entranceSide * campusWidth * 0.4;
  const entranceZ = campusDepth * (0.34 - signals.longitude * 0.08);
  const towerHeight = 0.24 + signals.risk * 0.34;
  elements.push(
    facilityElement(
      "gatehouse",
      entranceX,
      -0.11,
      entranceZ,
      0.14 + signals.throughput * 0.04,
      0.14,
      0.11,
    ),
    facilityElement(
      "tower",
      entranceX,
      -0.18 + towerHeight / 2,
      -campusDepth * 0.38,
      0.045,
      towerHeight,
      0.045,
    ),
    facilityElement(
      "beacon",
      entranceX,
      -0.15 + towerHeight,
      -campusDepth * 0.38,
      0.075,
      0.06,
      0.075,
    ),
  );
  return [entranceSide * campusWidth * 0.49, entranceZ];
}

function buildTransportRoutes({
  entrance,
  topology,
  storageSlots,
  hubRisk,
  routeRisk,
}: {
  entrance: FacilityRoutePoint;
  topology: FacilityTopology;
  storageSlots: number;
  hubRisk: boolean;
  routeRisk: boolean;
}): readonly FacilityTransportRoute[] {
  const dock = dockRoutePoint(topology.dockLine);
  const yard = yardRoutePoint(topology.yard, storageSlots);
  return [
    {
      kind: "gate-to-dock",
      status: routeRisk ? "risk" : "active",
      points: connectViaSpine(entrance, dock, topology.spine),
    },
    {
      kind: "dock-to-yard",
      status: hubRisk ? "risk" : "active",
      points: connectViaSpine(dock, yard, topology.spine),
    },
    {
      kind: "yard-to-gate",
      status: "active",
      points: connectViaSpine(yard, entrance, offsetSpine(topology.spine)),
    },
  ];
}

export function buildFacilityTransportSegments(
  routes: readonly FacilityTransportRoute[],
): readonly FacilityTransportSegment[] {
  const segments = new Map<string, FacilityTransportSegment>();
  for (const route of routes) {
    route.points.slice(1).forEach((point, index) => {
      const previous = route.points[index];
      if (previous === undefined) {
        return;
      }
      const key = physicalSegmentKey(previous, point);
      const current = segments.get(key);
      if (current === undefined) {
        segments.set(key, {
          id: key,
          status: route.status,
          routeKinds: [route.kind],
          from: previous,
          to: point,
        });
        return;
      }
      const routeKinds = current.routeKinds.includes(route.kind)
        ? current.routeKinds
        : [...current.routeKinds, route.kind];
      segments.set(key, {
        ...current,
        status:
          current.status === "risk" || route.status === "risk"
            ? "risk"
            : "active",
        routeKinds,
      });
    });
  }
  return [...segments.values()];
}

function buildFacilityVehiclePath(
  routes: readonly FacilityTransportRoute[],
): FacilityVehiclePath {
  const [inbound, transfer, outbound] = routes;
  if (
    inbound === undefined ||
    transfer === undefined ||
    outbound === undefined
  ) {
    throw new Error("facility transport chain requires three routes");
  }
  const joined = [
    ...inbound.points,
    ...transfer.points.slice(1),
    ...outbound.points.slice(1),
  ];
  const [first, second, ...rest] = joined;
  if (first === undefined || second === undefined) {
    throw new Error("facility vehicle path requires at least two points");
  }
  const points = [first, second, ...rest] satisfies FacilityVehiclePath["points"];
  const totalLength = facilityPathLength(points);
  return {
    points,
    dockPhase:
      totalLength === 0 ? 0 : facilityPathLength(inbound.points) / totalLength,
  };
}

function physicalSegmentKey(
  from: FacilityRoutePoint,
  to: FacilityRoutePoint,
): string {
  return [from, to]
    .map(([x, z]) => `${x.toFixed(9)},${z.toFixed(9)}`)
    .sort()
    .join("|");
}

function dockRoutePoint(dockLine: DockLine): FacilityRoutePoint {
  return dockLine.axis === "x"
    ? [
        dockLine.x,
        dockLine.z + dockLine.facing * dockLine.apronDepth * 0.22,
      ]
    : [
        dockLine.x + dockLine.facing * dockLine.apronDepth * 0.22,
        dockLine.z,
      ];
}

function yardRoutePoint(
  yard: YardGrid,
  storageSlots: number,
): FacilityRoutePoint {
  const columns = Math.min(yard.columns, storageSlots);
  const rows = Math.ceil(storageSlots / yard.columns);
  return [
    yard.x + ((columns - 1) * yard.xStep) / 2,
    yard.z + ((rows - 1) * yard.zStep) / 2,
  ];
}

function connectViaSpine(
  from: FacilityRoutePoint,
  to: FacilityRoutePoint,
  spine: FacilityRoadSpine,
): [
  FacilityRoutePoint,
  FacilityRoutePoint,
  FacilityRoutePoint,
  FacilityRoutePoint,
] {
  return spine.axis === "z"
    ? [
        from,
        [spine.x, from[1]],
        [spine.x, to[1]],
        to,
      ]
    : [
        from,
        [from[0], spine.z],
        [to[0], spine.z],
        to,
      ];
}

function offsetSpine(spine: FacilityRoadSpine): FacilityRoadSpine {
  return spine.axis === "z"
    ? { axis: "z", x: spine.x - 0.045 }
    : { axis: "x", z: spine.z - 0.045 };
}

function buildFacilityVehicles({
  hub,
  primaryRoute,
  signals,
  vehiclePath,
}: {
  hub: HubOverview;
  primaryRoute: PrimaryRoute | undefined;
  signals: LayoutSignals;
  vehiclePath: FacilityVehiclePath;
}): readonly FacilityVehicle[] {
  const vehicleCount = Math.min(
    6,
    Math.max(
      1,
      1 +
        Math.round(signals.activity * 3) +
        Math.min(hub.anomalies, 1) +
        Math.min(hub.handling, 1),
    ),
  );
  const alertCount = Math.min(hub.anomalies, vehicleCount, 2);
  const loadingCount = Math.min(
    hub.handling,
    vehicleCount - alertCount,
    2,
  );
  const seed = stableSeed(
    `${hub.hub_id}:${primaryRoute?.route.route_id ?? "local"}`,
  );
  const movingSpeed =
    0.04 + signals.throughput * 0.025 + (seed % 5) * 0.002;

  return Array.from({ length: vehicleCount }, (_, index) => {
    const state =
      index < alertCount
        ? "alert"
        : index < alertCount + loadingCount
          ? "loading"
          : "moving";
    return {
      id: `${hub.hub_id}-VEH-${String(index + 1).padStart(2, "0")}`,
      state,
      phase:
        state === "loading"
          ? normalizeProgress(
              vehiclePath.dockPhase +
                (index - alertCount - (loadingCount - 1) / 2) * 0.018,
            )
          : ((seed % 997) / 997 + index / vehicleCount) % 1,
      speed: state === "loading" ? 0 : movingSpeed,
    };
  });
}

function stableSeed(value: string): number {
  let hash = 2166136261;
  for (const character of value) {
    hash ^= character.codePointAt(0) ?? 0;
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

export function sampleFacilityRoute(
  route: FacilityTransportRoute,
  progress: number,
): FacilityRouteSample {
  return sampleFacilityPathPoints(route.points, progress, false);
}

export function sampleFacilityVehiclePath(
  path: FacilityVehiclePath,
  progress: number,
): FacilityRouteSample {
  return sampleFacilityPathPoints(path.points, progress, true);
}

function sampleFacilityPathPoints(
  points: FacilityVehiclePath["points"],
  progress: number,
  closed: boolean,
): FacilityRouteSample {
  const segmentLengths = points.slice(1).map((point, index) => {
    const previous = points[index];
    if (previous === undefined) {
      return 0;
    }
    return Math.hypot(point[0] - previous[0], point[1] - previous[1]);
  });
  const totalLength = segmentLengths.reduce(
    (total, length) => total + length,
    0,
  );
  if (totalLength === 0) {
    const [x, z] = points[0];
    return { x, z, rotationY: 0 };
  }
  const normalizedProgress = normalizeProgress(progress);
  const position = sampleFacilityPathPosition(
    points,
    segmentLengths,
    totalLength,
    normalizedProgress,
  );
  const headingWindow = Math.min(0.045 / totalLength, 0.04);
  const beforeProgress = closed
    ? normalizeProgress(normalizedProgress - headingWindow)
    : Math.max(0, normalizedProgress - headingWindow);
  const afterProgress = closed
    ? normalizeProgress(normalizedProgress + headingWindow)
    : Math.min(1, normalizedProgress + headingWindow);
  const before = sampleFacilityPathPosition(
    points,
    segmentLengths,
    totalLength,
    beforeProgress,
  );
  const after = sampleFacilityPathPosition(
    points,
    segmentLengths,
    totalLength,
    afterProgress,
  );
  return {
    ...position,
    rotationY: Math.atan2(after.x - before.x, after.z - before.z),
  };
}

function sampleFacilityPathPosition(
  points: FacilityVehiclePath["points"],
  segmentLengths: readonly number[],
  totalLength: number,
  progress: number,
): Pick<FacilityRouteSample, "x" | "z"> {
  let distance = progress * totalLength;
  for (let index = 0; index < segmentLengths.length; index += 1) {
    const length = segmentLengths[index] ?? 0;
    const from = points[index];
    const to = points[index + 1];
    if (from === undefined || to === undefined) {
      continue;
    }
    if (distance <= length || index === segmentLengths.length - 1) {
      const ratio = length === 0 ? 0 : Math.min(distance / length, 1);
      return {
        x: from[0] + (to[0] - from[0]) * ratio,
        z: from[1] + (to[1] - from[1]) * ratio,
      };
    }
    distance -= length;
  }
  const [x, z] = points[points.length - 1];
  return { x, z };
}

function facilityPathLength(
  points: readonly FacilityRoutePoint[],
): number {
  return points.slice(1).reduce((total, point, index) => {
    const previous = points[index];
    if (previous === undefined) {
      return total;
    }
    return total + Math.hypot(
      point[0] - previous[0],
      point[1] - previous[1],
    );
  }, 0);
}

function normalizeProgress(progress: number): number {
  return ((progress % 1) + 1) % 1;
}

function facilityElement(
  kind: FacilityElementKind,
  x: number,
  centerY: number,
  z: number,
  width: number,
  height: number,
  depth: number,
  rotationY = 0,
): FacilityElement {
  return {
    kind,
    x,
    centerY,
    z,
    width,
    height,
    depth,
    rotationY,
  };
}

function findPrimaryRoute(
  hubID: string,
  routes: readonly RouteOverview[],
): PrimaryRoute | undefined {
  let selected: PrimaryRoute | undefined;
  for (const route of routes) {
    const entranceSide =
      route.origin_hub_id === hubID
        ? 1
        : route.destination_hub_id === hubID
          ? -1
          : undefined;
    if (entranceSide === undefined) {
      continue;
    }
    if (
      selected === undefined ||
      routePrecedes(route, selected.route)
    ) {
      selected = { route, entranceSide };
    }
  }
  return selected;
}

function routePrecedes(
  candidate: RouteOverview,
  current: RouteOverview,
): boolean {
  if (candidate.waybills !== current.waybills) {
    return candidate.waybills > current.waybills;
  }
  if (candidate.anomalies !== current.anomalies) {
    return candidate.anomalies > current.anomalies;
  }
  if (candidate.distance_km !== current.distance_km) {
    return candidate.distance_km > current.distance_km;
  }
  return candidate.route_id.localeCompare(current.route_id) < 0;
}

function normalize(value: number, minimumValue: number, maximumValue: number) {
  return Math.min(
    1,
    Math.max(
      0,
      (value - minimumValue) / Math.max(maximumValue - minimumValue, 1),
    ),
  );
}

function minimum(values: readonly number[], fallback: number): number {
  return values.length === 0 ? fallback : Math.min(...values);
}

function maximum(values: readonly number[], fallback: number): number {
  return values.length === 0 ? fallback : Math.max(...values);
}

function distributedPosition(
  start: number,
  end: number,
  index: number,
  count: number,
): number {
  if (count <= 1) {
    return (start + end) / 2;
  }
  return start + ((end - start) * index) / (count - 1);
}

function roundSignature(value: number): string {
  return value.toFixed(3);
}
