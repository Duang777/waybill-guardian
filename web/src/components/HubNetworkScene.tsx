import { Canvas, useFrame, useThree, type ThreeEvent } from "@react-three/fiber";
import {
  BufferGeometry,
  Color,
  Float32BufferAttribute,
  InstancedMesh,
  MathUtils,
  Matrix4,
  Object3D,
  OrthographicCamera,
  QuadraticBezierCurve3,
  Vector3,
} from "three";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
} from "react";
import type {
  AnomalyOverview,
  HubOverview,
  RouteOverview,
  WaybillID,
} from "../api";
import {
  buildFacilityLayouts,
  sampleFacilityVehiclePath,
  type FacilityElementKind,
  type FacilityLayout,
  type FacilityLayoutKind,
  type FacilityTransportSegment,
  type FacilityVehicle,
  type FacilityVehiclePath,
  type FacilityVehicleState,
} from "./facilityLayout";

type HubNetworkSceneProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
  selectedHubID: string | null;
  cameraPreset: FacilityCameraPreset;
  zoomScale: number;
  facilitySelection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelectHub: (hubID: string) => void;
  onSelectFacilityObject: (selection: FacilitySceneSelection) => void;
  onSelectWaybill: (waybillID: WaybillID) => void;
  onFailure: () => void;
  onReady: () => void;
  onDrawCalls: (drawCalls: number) => void;
  onStats: (stats: SceneStats) => void;
};

export type SceneStats = {
  mode: SceneMode;
  hubs: number;
  routes: number;
  markers: number;
  facilityParts: number;
  detailParts: number;
  dockBays: number;
  storageSlots: number;
  occupiedSlots: number;
  warehouseCount: number;
  transportRoutes: number;
  transportSegments: number;
  vehicles: number;
  movingVehicles: number;
  loadingVehicles: number;
  alertVehicles: number;
  layoutKind: FacilityLayoutKind | null;
  layoutLabel: string;
  layoutSignature: string;
  archetypes: Record<HubArchetype, number>;
};

export type SceneMode = "network" | "facility";

export type FacilityCameraPreset = "overview" | "follow" | "risk";

export type FacilitySceneSelection =
  | { kind: "vehicle"; id: string }
  | { kind: "route"; id: string }
  | { kind: "alert"; id: "facility-alert" };

export type HubArchetype =
  | "local-depot"
  | "regional-cross-dock"
  | "gateway-campus"
  | "yard-terminal";

type SceneHub = {
  hub: HubOverview;
  position: Vector3;
  archetype: HubArchetype;
  scale: number;
  activity: number;
  cargoSlots: number;
  signalHeight: number;
  rotationY: number;
  priority: boolean;
};

type HubPartKind = "pad" | "structure" | "roof" | "cargo" | "signal";

type HubPartTemplate = {
  kind: Exclude<HubPartKind, "cargo" | "signal">;
  offset: readonly [number, number, number];
  size: readonly [number, number, number];
};

type HubPart = {
  kind: HubPartKind;
  offset: readonly [number, number, number];
  size: readonly [number, number, number];
  sceneHub: SceneHub;
  color: string;
};

type CapacityBreaks = {
  lower: number;
  middle: number;
  upper: number;
};

type SceneRoute = {
  route: RouteOverview;
  curve: QuadraticBezierCurve3;
  anomaly: AnomalyOverview | undefined;
};

type FlowMarker = {
  route: SceneRoute;
  phase: number;
  speed: number;
};

type SceneModel = {
  hubs: readonly SceneHub[];
  hubsByID: ReadonlyMap<string, SceneHub>;
  facilityParts: readonly HubPart[];
  routes: readonly SceneRoute[];
  markers: readonly FlowMarker[];
  riskHubs: readonly SceneHub[];
  facilityLayouts: ReadonlyMap<string, FacilityLayout>;
};

type NetworkSceneView = {
  mode: "network";
  selectedHub: undefined;
  detail: undefined;
  hubs: readonly SceneHub[];
  facilityParts: readonly HubPart[];
  routes: readonly SceneRoute[];
  markers: readonly FlowMarker[];
  riskHubs: readonly SceneHub[];
};

type FacilitySceneView = {
  mode: "facility";
  selectedHub: SceneHub;
  detail: FacilityLayout;
  hubs: readonly [SceneHub];
  facilityParts: readonly [];
  routes: readonly [];
  markers: readonly [];
  riskHubs: readonly [];
};

type SceneView = NetworkSceneView | FacilitySceneView;

const coordinateBounds = {
  minLongitude: 73,
  maxLongitude: 135,
  minLatitude: 18,
  maxLatitude: 54,
};

const sceneWidth = 22;
const sceneDepth = 12;
const maxFlowMarkersPerRoute = 4;
const networkCameraOffset: [number, number, number] = [2.4, 22, 9.5];
const networkFacilityScale = 0.68;
const networkZoomMultiplier = 1.24;
const criticalRouteRiskThreshold = 50;
const maxEmphasizedRiskHubs = 6;
const transform = new Object3D();
const markerTransform = new Matrix4();
const markerPoint = new Vector3();
const facilityTransform = new Object3D();
const facilityPartTransform = new Object3D();
const facilityPartMatrix = new Matrix4();
const facilityPartColor = new Color();
const detailPartTransform = new Object3D();
const detailFacilityScale = 3.2;
const detailRouteTransform = new Object3D();
const detailVehicleRootTransform = new Object3D();
const detailVehiclePartTransform = new Object3D();
const detailVehiclePartMatrix = new Matrix4();
const sceneUp = new Vector3(0, 1, 0);
const overviewCameraOffset = new Vector3(5.2, 7.8, 6.3);
const followCameraOffset = new Vector3(3.1, 4.7, 3.8);
const riskCameraOffset = new Vector3(3.5, 5.2, 4.2);

const hubArchetypes = [
  "local-depot",
  "regional-cross-dock",
  "gateway-campus",
  "yard-terminal",
] satisfies readonly HubArchetype[];

const structureColors = {
  "local-depot": "#427b75",
  "regional-cross-dock": "#376f69",
  "gateway-campus": "#2d655f",
  "yard-terminal": "#245852",
} satisfies Record<HubArchetype, string>;

const facilityTemplates = {
  "local-depot": [
    { kind: "pad", offset: [0, -0.16, 0], size: [0.34, 0.04, 0.28] },
    {
      kind: "structure",
      offset: [-0.035, -0.06, -0.025],
      size: [0.2, 0.16, 0.12],
    },
    {
      kind: "roof",
      offset: [-0.035, 0.032, -0.025],
      size: [0.22, 0.024, 0.14],
    },
    {
      kind: "structure",
      offset: [-0.035, -0.105, 0.07],
      size: [0.16, 0.05, 0.04],
    },
  ],
  "regional-cross-dock": [
    { kind: "pad", offset: [0, -0.16, 0], size: [0.42, 0.04, 0.34] },
    {
      kind: "structure",
      offset: [-0.045, -0.052, -0.075],
      size: [0.29, 0.176, 0.1],
    },
    {
      kind: "roof",
      offset: [-0.045, 0.049, -0.075],
      size: [0.31, 0.026, 0.12],
    },
    {
      kind: "structure",
      offset: [0.055, -0.075, 0.085],
      size: [0.18, 0.13, 0.09],
    },
    {
      kind: "roof",
      offset: [0.055, 0.003, 0.085],
      size: [0.2, 0.024, 0.11],
    },
    {
      kind: "structure",
      offset: [-0.045, -0.108, 0.005],
      size: [0.24, 0.044, 0.038],
    },
  ],
  "gateway-campus": [
    { kind: "pad", offset: [0, -0.16, 0], size: [0.48, 0.04, 0.38] },
    {
      kind: "structure",
      offset: [-0.06, -0.045, -0.095],
      size: [0.26, 0.19, 0.09],
    },
    {
      kind: "roof",
      offset: [-0.06, 0.064, -0.095],
      size: [0.28, 0.028, 0.11],
    },
    {
      kind: "structure",
      offset: [-0.06, -0.055, 0.095],
      size: [0.26, 0.17, 0.09],
    },
    {
      kind: "roof",
      offset: [-0.06, 0.044, 0.095],
      size: [0.28, 0.028, 0.11],
    },
    {
      kind: "structure",
      offset: [0.155, 0.03, -0.01],
      size: [0.065, 0.34, 0.065],
    },
    {
      kind: "roof",
      offset: [0.155, 0.212, -0.01],
      size: [0.085, 0.024, 0.085],
    },
    {
      kind: "roof",
      offset: [0.105, -0.102, 0.13],
      size: [0.13, 0.05, 0.05],
    },
  ],
  "yard-terminal": [
    { kind: "pad", offset: [0, -0.16, 0], size: [0.52, 0.04, 0.42] },
    {
      kind: "structure",
      offset: [-0.085, -0.048, -0.1],
      size: [0.29, 0.184, 0.12],
    },
    {
      kind: "roof",
      offset: [-0.085, 0.057, -0.1],
      size: [0.31, 0.026, 0.14],
    },
    {
      kind: "structure",
      offset: [0.105, -0.072, 0.015],
      size: [0.16, 0.136, 0.09],
    },
    {
      kind: "roof",
      offset: [0.105, 0.009, 0.015],
      size: [0.18, 0.026, 0.11],
    },
    {
      kind: "structure",
      offset: [0.175, 0.02, -0.13],
      size: [0.065, 0.32, 0.065],
    },
    {
      kind: "roof",
      offset: [0.175, 0.192, -0.13],
      size: [0.085, 0.024, 0.085],
    },
    {
      kind: "structure",
      offset: [-0.075, -0.108, 0.005],
      size: [0.22, 0.044, 0.04],
    },
  ],
} satisfies Record<HubArchetype, readonly HubPartTemplate[]>;

const cargoPositions = {
  "local-depot": [
    [0.105, 0.015],
    [0.105, 0.075],
    [0.045, 0.115],
  ],
  "regional-cross-dock": [
    [-0.12, 0.115],
    [-0.025, 0.115],
    [0.07, 0.135],
  ],
  "gateway-campus": [
    [0.05, -0.095],
    [0.05, 0],
    [0.05, 0.095],
  ],
  "yard-terminal": [
    [-0.085, 0.12],
    [0.015, 0.12],
    [0.115, 0.13],
  ],
} satisfies Record<
  HubArchetype,
  readonly (readonly [number, number])[]
>;

const signalPositions = {
  "local-depot": [-0.135, -0.105],
  "regional-cross-dock": [-0.175, -0.135],
  "gateway-campus": [-0.205, -0.155],
  "yard-terminal": [-0.225, -0.175],
} satisfies Record<HubArchetype, readonly [number, number]>;

export default function HubNetworkScene({
  hubs,
  routes,
  anomalies,
  selectedHubID,
  cameraPreset,
  zoomScale,
  facilitySelection,
  reducedMotion,
  paused,
  onSelectHub,
  onSelectFacilityObject,
  onSelectWaybill,
  onFailure,
  onReady,
  onDrawCalls,
  onStats,
}: HubNetworkSceneProps) {
  const model = useMemo(
    () => buildSceneModel(hubs, routes, anomalies),
    [anomalies, hubs, routes],
  );
  const view = useMemo(
    () => buildSceneView(model, selectedHubID),
    [model, selectedHubID],
  );

  useEffect(() => {
    onStats({
      mode: view.mode,
      hubs: view.hubs.length,
      routes: view.routes.length,
      markers: view.markers.length,
      facilityParts: view.facilityParts.length,
      detailParts: view.detail?.elements.length ?? 0,
      dockBays: view.detail?.dockBays ?? 0,
      storageSlots: view.detail?.storageSlots ?? 0,
      occupiedSlots: view.detail?.occupiedSlots ?? 0,
      warehouseCount: view.detail?.warehouseCount ?? 0,
      transportRoutes: view.detail?.transportRoutes.length ?? 0,
      transportSegments: view.detail?.transportSegments.length ?? 0,
      vehicles: view.detail?.vehicles.length ?? 0,
      movingVehicles:
        view.detail?.vehicles.filter((vehicle) => vehicle.state === "moving")
          .length ?? 0,
      loadingVehicles:
        view.detail?.vehicles.filter((vehicle) => vehicle.state === "loading")
          .length ?? 0,
      alertVehicles:
        view.detail?.vehicles.filter((vehicle) => vehicle.state === "alert")
          .length ?? 0,
      layoutKind: view.detail?.kind ?? null,
      layoutLabel: view.detail?.label ?? "",
      layoutSignature: view.detail?.signature ?? "",
      archetypes: countArchetypes(view.hubs),
    });
  }, [onStats, view]);

  return (
    <Canvas
      orthographic
      camera={{
        position: networkCameraOffset,
        near: 0.1,
        far: 100,
        zoom: 34,
      }}
      dpr={[1, 1.5]}
      frameloop={reducedMotion || paused ? "demand" : "always"}
      gl={{
        alpha: false,
        antialias: true,
        powerPreference: "high-performance",
      }}
      onCreated={({ gl }) => {
        gl.setClearColor("#f1f2ee", 1);
      }}
    >
      <color attach="background" args={["#f1f2ee"]} />
      <ambientLight intensity={2.4} />
      <hemisphereLight args={["#ffffff", "#bfc7c3", 1.35]} />
      <directionalLight position={[8, 14, 10]} intensity={2.1} />
      <WebGLContextObserver onFailure={onFailure} />

      {view.mode === "network" ? (
        <>
          <StrategyTable />
          <RouteLines routes={view.routes} />
          <HubFacilities
            parts={view.facilityParts}
            scaleMultiplier={networkFacilityScale}
            interactive
            onSelectHub={onSelectHub}
          />
          <FlowMarkers
            markers={view.markers}
            reducedMotion={reducedMotion}
            paused={paused}
            onSelectWaybill={onSelectWaybill}
          />
          <RiskRings
            hubs={view.riskHubs}
            reducedMotion={reducedMotion}
            paused={paused}
          />
        </>
      ) : (
        <FacilityDetailGround
          hub={view.selectedHub}
          detail={view.detail}
          selection={facilitySelection}
          reducedMotion={reducedMotion}
          paused={paused}
          onSelect={onSelectFacilityObject}
        />
      )}
      <CameraRig
        networkHubs={model.hubs}
        selectedHub={view.selectedHub}
        detail={view.detail}
        preset={cameraPreset}
        zoomScale={zoomScale}
        selection={facilitySelection}
        reducedMotion={reducedMotion}
        paused={paused}
      />
      <SceneReporter
        reportKey={selectedHubID ?? "network"}
        onReady={onReady}
        onDrawCalls={onDrawCalls}
      />
    </Canvas>
  );
}

function StrategyTable() {
  return (
    <group>
      <mesh position={[0, -0.265, 0]}>
        <boxGeometry args={[24.8, 0.08, 14.4]} />
        <meshStandardMaterial color="#ecefeb" roughness={1} />
      </mesh>
      <gridHelper
        args={[24, 16, "#b6c0bd", "#dce1de"]}
        position={[0, -0.21, 0]}
        scale={[1, 1, 0.58]}
      />
    </group>
  );
}

function HubFacilities({
  parts,
  scaleMultiplier,
  interactive,
  onSelectHub,
}: {
  parts: readonly HubPart[];
  scaleMultiplier: number;
  interactive: boolean;
  onSelectHub: (hubID: string) => void;
}) {
  const pads = parts.filter((part) => part.kind === "pad");
  const structures = parts.filter((part) => part.kind === "structure");
  const roofs = parts.filter((part) => part.kind === "roof");
  const cargo = parts.filter((part) => part.kind === "cargo");
  const signals = parts.filter((part) => part.kind === "signal");

  return (
    <group>
      <FacilityPartInstances
        parts={pads}
        scaleMultiplier={scaleMultiplier}
        roughness={0.94}
        interactive={interactive}
        onSelectHub={onSelectHub}
      />
      <FacilityPartInstances
        parts={structures}
        scaleMultiplier={scaleMultiplier}
        roughness={0.72}
        interactive={interactive}
        onSelectHub={onSelectHub}
      />
      <FacilityPartInstances
        parts={roofs}
        scaleMultiplier={scaleMultiplier}
        roughness={0.88}
        interactive={interactive}
        onSelectHub={onSelectHub}
      />
      <FacilityPartInstances
        parts={cargo}
        scaleMultiplier={scaleMultiplier}
        roughness={0.8}
        interactive={interactive}
        onSelectHub={onSelectHub}
      />
      {signals.length > 0 && (
        <FacilityPartInstances
          parts={signals}
          scaleMultiplier={scaleMultiplier}
          roughness={0.62}
          interactive={interactive}
          onSelectHub={onSelectHub}
        />
      )}
    </group>
  );
}

function FacilityPartInstances({
  parts,
  scaleMultiplier,
  roughness,
  interactive,
  onSelectHub,
}: {
  parts: readonly HubPart[];
  scaleMultiplier: number;
  roughness: number;
  interactive: boolean;
  onSelectHub: (hubID: string) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const { gl } = useThree();

  useLayoutEffect(() => {
    if (mesh.current === null) {
      return;
    }
    parts.forEach((part, index) => {
      mesh.current?.setMatrixAt(
        index,
        composeFacilityPartMatrix(part, scaleMultiplier),
      );
      facilityPartColor.set(part.color);
      mesh.current?.setColorAt(index, facilityPartColor);
    });
    mesh.current.instanceMatrix.needsUpdate = true;
    if (mesh.current.instanceColor !== null) {
      mesh.current.instanceColor.needsUpdate = true;
    }
  }, [parts, scaleMultiplier]);

  const setCursor = useCallback(
    (cursor: "default" | "pointer") => {
      gl.domElement.style.cursor = cursor;
    },
    [gl],
  );

  const select = useCallback(
    (event: ThreeEvent<PointerEvent>) => {
      event.stopPropagation();
      if (event.instanceId !== undefined) {
        const part = parts[event.instanceId];
        if (part !== undefined) {
          setCursor("default");
          onSelectHub(part.sceneHub.hub.hub_id);
        }
      }
    },
    [onSelectHub, parts, setCursor],
  );

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, parts.length]}
      frustumCulled={false}
      raycast={interactive ? undefined : () => undefined}
      onClick={interactive ? select : undefined}
      onPointerOver={
        interactive
          ? (event) => {
              event.stopPropagation();
              setCursor("pointer");
            }
          : undefined
      }
      onPointerOut={interactive ? () => setCursor("default") : undefined}
    >
      <boxGeometry args={[1, 1, 1]} />
      <meshStandardMaterial
        color="#ffffff"
        roughness={roughness}
        metalness={0.02}
      />
    </instancedMesh>
  );
}

function composeFacilityPartMatrix(
  part: HubPart,
  scaleMultiplier: number,
): Matrix4 {
  facilityTransform.position.copy(part.sceneHub.position);
  facilityTransform.rotation.set(0, part.sceneHub.rotationY, 0);
  facilityTransform.scale.setScalar(part.sceneHub.scale * scaleMultiplier);
  facilityTransform.updateMatrix();

  facilityPartTransform.position.set(...part.offset);
  facilityPartTransform.rotation.set(0, 0, 0);
  facilityPartTransform.scale.set(...part.size);
  facilityPartTransform.updateMatrix();

  return facilityPartMatrix.multiplyMatrices(
    facilityTransform.matrix,
    facilityPartTransform.matrix,
  );
}

function FacilityDetailGround({
  hub,
  detail,
  selection,
  reducedMotion,
  paused,
  onSelect,
}: {
  hub: SceneHub;
  detail: FacilityLayout;
  selection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelect: (selection: FacilitySceneSelection) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const { gl } = useThree();

  useLayoutEffect(() => {
    if (mesh.current === null) {
      return;
    }
    detail.elements.forEach((element, index) => {
      detailPartTransform.position.set(
        element.x,
        element.centerY,
        element.z,
      );
      detailPartTransform.rotation.set(0, element.rotationY, 0);
      detailPartTransform.scale.set(
        element.width,
        element.height,
        element.depth,
      );
      detailPartTransform.updateMatrix();
      mesh.current?.setMatrixAt(index, detailPartTransform.matrix);
      facilityPartColor.set(
        detailElementColor(
          element.kind,
          hub,
          selection !== null && element.kind !== "beacon",
        ),
      );
      mesh.current?.setColorAt(index, facilityPartColor);
    });
    mesh.current.instanceMatrix.needsUpdate = true;
    if (mesh.current.instanceColor !== null) {
      mesh.current.instanceColor.needsUpdate = true;
    }
  }, [detail.elements, hub, selection]);

  const setCursor = useCallback(
    (cursor: "default" | "pointer") => {
      gl.domElement.style.cursor = cursor;
    },
    [gl],
  );

  return (
    <group
      position={[hub.position.x, 0, hub.position.z]}
      rotation={[0, hub.rotationY, 0]}
      scale={hub.scale * detailFacilityScale}
    >
      <instancedMesh
        ref={mesh}
        args={[undefined, undefined, detail.elements.length]}
        frustumCulled={false}
        onClick={(event) => {
          event.stopPropagation();
          const element =
            event.instanceId === undefined
              ? undefined
              : detail.elements[event.instanceId];
          if (element?.kind === "beacon") {
            setCursor("default");
            onSelect({ kind: "alert", id: "facility-alert" });
          }
        }}
        onPointerMove={(event) => {
          const element =
            event.instanceId === undefined
              ? undefined
              : detail.elements[event.instanceId];
          setCursor(element?.kind === "beacon" ? "pointer" : "default");
        }}
        onPointerOut={() => setCursor("default")}
      >
        <boxGeometry args={[1, 1, 1]} />
        <meshStandardMaterial
          color="#ffffff"
          roughness={0.9}
          metalness={0.01}
        />
      </instancedMesh>
      <FacilityTransportRoutes
        segments={detail.transportSegments}
        selection={selection}
        onSelect={onSelect}
      />
      <FacilityVehicleInstances
        path={detail.vehiclePath}
        vehicles={detail.vehicles}
        selection={selection}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelect={onSelect}
      />
    </group>
  );
}

function FacilityTransportRoutes({
  segments,
  selection,
  onSelect,
}: {
  segments: readonly FacilityTransportSegment[];
  selection: FacilitySceneSelection | null;
  onSelect: (selection: FacilitySceneSelection) => void;
}) {
  return (
    <FacilityRouteSegmentInstances
      segments={segments}
      selection={selection}
      onSelect={onSelect}
    />
  );
}

function FacilityRouteSegmentInstances({
  segments,
  selection,
  onSelect,
}: {
  segments: readonly FacilityTransportSegment[];
  selection: FacilitySceneSelection | null;
  onSelect: (selection: FacilitySceneSelection) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const { gl } = useThree();

  useLayoutEffect(() => {
    if (mesh.current === null) {
      return;
    }
    segments.forEach((segment, index) => {
      const deltaX = segment.to[0] - segment.from[0];
      const deltaZ = segment.to[1] - segment.from[1];
      const length = Math.hypot(deltaX, deltaZ);
      detailRouteTransform.position.set(
        (segment.from[0] + segment.to[0]) / 2,
        -0.143,
        (segment.from[1] + segment.to[1]) / 2,
      );
      detailRouteTransform.rotation.set(
        0,
        Math.atan2(deltaX, deltaZ),
        0,
      );
      const selected =
        selection?.kind === "route" && selection.id === segment.id;
      detailRouteTransform.scale.set(
        selected ? 0.052 : segment.status === "risk" ? 0.034 : 0.024,
        selected ? 0.018 : 0.012,
        length,
      );
      detailRouteTransform.updateMatrix();
      mesh.current?.setMatrixAt(index, detailRouteTransform.matrix);
      facilityPartColor.set(
        segment.status === "risk"
          ? "#df3f30"
          : selection === null || selected
            ? "#0b746d"
            : "#aab5b1",
      );
      mesh.current?.setColorAt(index, facilityPartColor);
    });
    mesh.current.instanceMatrix.needsUpdate = true;
    if (mesh.current.instanceColor !== null) {
      mesh.current.instanceColor.needsUpdate = true;
    }
  }, [segments, selection]);

  if (segments.length === 0) {
    return null;
  }

  const setCursor = (cursor: "default" | "pointer") => {
    gl.domElement.style.cursor = cursor;
  };

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, segments.length]}
      frustumCulled={false}
      onClick={(event) => {
        event.stopPropagation();
        const segment =
          event.instanceId === undefined
            ? undefined
            : segments[event.instanceId];
        if (segment !== undefined) {
          setCursor("default");
          onSelect({ kind: "route", id: segment.id });
        }
      }}
      onPointerOver={(event) => {
        event.stopPropagation();
        setCursor("pointer");
      }}
      onPointerOut={() => setCursor("default")}
    >
      <boxGeometry args={[1, 1, 1]} />
      <meshBasicMaterial color="#ffffff" toneMapped={false} />
    </instancedMesh>
  );
}

function FacilityVehicleInstances({
  path,
  vehicles,
  selection,
  reducedMotion,
  paused,
  onSelect,
}: {
  path: FacilityVehiclePath;
  vehicles: readonly FacilityVehicle[];
  selection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelect: (selection: FacilitySceneSelection) => void;
}) {
  const bodyMesh = useRef<InstancedMesh>(null);
  const cabinMesh = useRef<InstancedMesh>(null);
  const chassisMesh = useRef<InstancedMesh>(null);
  const elapsed = useRef(0);
  const selectionRef = useRef(selection);
  selectionRef.current = selection;
  const { gl } = useThree();

  const updateVehicles = useCallback(
    (time: number) => {
      const body = bodyMesh.current;
      const cabin = cabinMesh.current;
      const chassis = chassisMesh.current;
      if (
        body === null ||
        cabin === null ||
        chassis === null
      ) {
        return;
      }
      let firstPosition = "";
      vehicles.forEach((vehicle, index) => {
        const progress = vehicle.phase + time * vehicle.speed;
        const sample = sampleFacilityVehiclePath(path, progress);
        if (index === 0) {
          firstPosition =
            `${sample.x.toFixed(4)},${sample.z.toFixed(4)}`;
        }
        detailVehicleRootTransform.position.set(
          sample.x,
          -0.105,
          sample.z,
        );
        detailVehicleRootTransform.rotation.set(
          0,
          sample.rotationY,
          0,
        );
        const selected =
          selectionRef.current?.kind === "vehicle" &&
          selectionRef.current.id === vehicle.id;
        detailVehicleRootTransform.scale.setScalar(selected ? 1.18 : 1);
        detailVehicleRootTransform.updateMatrix();

        setVehiclePartMatrix(body, index, {
          x: 0,
          y: 0,
          z: 0,
          width: 0.058,
          height: 0.055,
          depth: 0.13,
        });
        setVehiclePartMatrix(cabin, index, {
          x: 0,
          y: 0.008,
          z: 0.042,
          width: 0.054,
          height: 0.067,
          depth: 0.046,
        });
        setVehiclePartMatrix(chassis, index, {
          x: 0,
          y: -0.032,
          z: -0.004,
          width: 0.068,
          height: 0.018,
          depth: 0.14,
        });
      });
      body.instanceMatrix.needsUpdate = true;
      cabin.instanceMatrix.needsUpdate = true;
      chassis.instanceMatrix.needsUpdate = true;
      if (import.meta.env.DEV) {
        gl.domElement.dataset.facilityVehiclePosition = firstPosition;
      }
    },
    [gl, path, vehicles],
  );

  useLayoutEffect(() => {
    elapsed.current = 0;
    updateVehicles(0);
  }, [updateVehicles, vehicles]);

  useLayoutEffect(() => {
    const body = bodyMesh.current;
    vehicles.forEach((vehicle, index) => {
      const selected =
        selection?.kind === "vehicle" && selection.id === vehicle.id;
      facilityPartColor.set(
        vehicleColor(
          vehicle.state,
          selected,
          selection !== null && !selected && vehicle.state !== "alert",
        ),
      );
      body?.setColorAt(index, facilityPartColor);
    });
    if (body?.instanceColor !== null && body?.instanceColor !== undefined) {
      body.instanceColor.needsUpdate = true;
    }
    updateVehicles(elapsed.current);
  }, [selection, updateVehicles, vehicles]);

  useFrame((_, delta) => {
    if (reducedMotion || paused) {
      return;
    }
    elapsed.current += Math.min(delta, 0.05);
    updateVehicles(elapsed.current);
  });

  return (
    <group>
      <instancedMesh
        ref={chassisMesh}
        args={[undefined, undefined, vehicles.length]}
        frustumCulled={false}
        raycast={() => undefined}
      >
        <boxGeometry args={[1, 1, 1]} />
        <meshStandardMaterial color="#253431" roughness={0.82} />
      </instancedMesh>
      <instancedMesh
        ref={bodyMesh}
        args={[undefined, undefined, vehicles.length]}
        frustumCulled={false}
        onClick={(event) => {
          event.stopPropagation();
          const vehicle =
            event.instanceId === undefined
              ? undefined
              : vehicles[event.instanceId];
          if (vehicle !== undefined) {
            gl.domElement.style.cursor = "default";
            onSelect({ kind: "vehicle", id: vehicle.id });
          }
        }}
        onPointerOver={(event) => {
          event.stopPropagation();
          gl.domElement.style.cursor = "pointer";
        }}
        onPointerOut={() => {
          gl.domElement.style.cursor = "default";
        }}
      >
        <boxGeometry args={[1, 1, 1]} />
        <meshStandardMaterial
          color="#ffffff"
          roughness={0.68}
          metalness={0.02}
        />
      </instancedMesh>
      <instancedMesh
        ref={cabinMesh}
        args={[undefined, undefined, vehicles.length]}
        frustumCulled={false}
        raycast={() => undefined}
      >
        <boxGeometry args={[1, 1, 1]} />
        <meshStandardMaterial color="#edf1ee" roughness={0.76} />
      </instancedMesh>
    </group>
  );
}

function setVehiclePartMatrix(
  mesh: InstancedMesh,
  index: number,
  part: {
    x: number;
    y: number;
    z: number;
    width: number;
    height: number;
    depth: number;
  },
) {
  detailVehiclePartTransform.position.set(part.x, part.y, part.z);
  detailVehiclePartTransform.rotation.set(0, 0, 0);
  detailVehiclePartTransform.scale.set(
    part.width,
    part.height,
    part.depth,
  );
  detailVehiclePartTransform.updateMatrix();
  detailVehiclePartMatrix.multiplyMatrices(
    detailVehicleRootTransform.matrix,
    detailVehiclePartTransform.matrix,
  );
  mesh.setMatrixAt(index, detailVehiclePartMatrix);
}

function vehicleColor(
  state: FacilityVehicleState,
  selected: boolean,
  dimmed: boolean,
): string {
  if (dimmed) {
    return "#aeb8b4";
  }
  switch (state) {
    case "moving":
      return selected ? "#087b74" : "#243b38";
    case "loading":
      return "#c58d2d";
    case "alert":
      return "#df3f30";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function detailElementColor(
  kind: FacilityElementKind,
  hub: SceneHub,
  dimmed: boolean,
): string {
  if (dimmed) {
    switch (kind) {
      case "ground":
        return "#dfe3e0";
      case "road":
      case "apron":
      case "perimeter":
      case "dock":
      case "slot":
        return "#bdc6c2";
      case "warehouse":
      case "gatehouse":
      case "tower":
        return "#93a29e";
      case "roof":
      case "marking":
        return "#f0f2ef";
      case "cargo":
        return "#b5bcb8";
      case "beacon":
        break;
      default: {
        const exhaustive: never = kind;
        return exhaustive;
      }
    }
  }
  switch (kind) {
    case "ground":
      return "#d9dfdc";
    case "perimeter":
      return "#64736f";
    case "road":
      return "#8f9b97";
    case "apron":
      return "#b7c0bc";
    case "warehouse":
      return structureColors[hub.archetype];
    case "roof":
      return "#eef1ee";
    case "dock":
      return "#263f3c";
    case "slot":
      return "#b8c2be";
    case "cargo":
      return hub.activity > 0.66 ? "#c58d2d" : "#4e7772";
    case "marking":
      return "#f4f3e8";
    case "gatehouse":
      return "#376f69";
    case "tower":
      return "#314c49";
    case "beacon":
      return hub.hub.anomalies > 0 ? "#df3f30" : "#427b75";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function RouteLines({ routes }: { routes: readonly SceneRoute[] }) {
  const normalGeometry = useMemo(
    () => createRouteGeometry(routes.filter((item) => item.route.anomalies === 0)),
    [routes],
  );
  const elevatedRiskGeometry = useMemo(
    () =>
      createRouteGeometry(
        routes.filter(
          (item) =>
            item.route.anomalies > 0 &&
            item.route.max_risk < criticalRouteRiskThreshold,
        ),
      ),
    [routes],
  );
  const criticalRiskGeometry = useMemo(
    () =>
      createRouteGeometry(
        routes.filter(
          (item) => item.route.max_risk >= criticalRouteRiskThreshold,
        ),
      ),
    [routes],
  );

  useEffect(
    () => () => {
      normalGeometry.dispose();
      elevatedRiskGeometry.dispose();
      criticalRiskGeometry.dispose();
    },
    [criticalRiskGeometry, elevatedRiskGeometry, normalGeometry],
  );

  return (
    <group>
      <lineSegments geometry={normalGeometry}>
        <lineBasicMaterial
          color="#687a76"
          transparent
          opacity={0.13}
          toneMapped={false}
        />
      </lineSegments>
      <lineSegments geometry={elevatedRiskGeometry}>
        <lineBasicMaterial
          color="#b88379"
          transparent
          opacity={0.14}
          toneMapped={false}
        />
      </lineSegments>
      <lineSegments geometry={criticalRiskGeometry}>
        <lineBasicMaterial
          color="#c93f32"
          transparent
          opacity={0.68}
          toneMapped={false}
        />
      </lineSegments>
    </group>
  );
}

function FlowMarkers({
  markers,
  reducedMotion,
  paused,
  onSelectWaybill,
}: {
  markers: readonly FlowMarker[];
  reducedMotion: boolean;
  paused: boolean;
  onSelectWaybill: (waybillID: WaybillID) => void;
}) {
  const normalMarkers = markers.filter(
    (marker) => marker.route.anomaly === undefined,
  );
  const elevatedRiskMarkers = markers.filter(
    (marker) =>
      marker.route.anomaly !== undefined &&
      marker.route.route.max_risk < criticalRouteRiskThreshold,
  );
  const criticalRiskMarkers = markers.filter(
    (marker) =>
      marker.route.anomaly !== undefined &&
      marker.route.route.max_risk >= criticalRouteRiskThreshold,
  );

  return (
    <group>
      <FlowMarkerInstances
        markers={normalMarkers}
        color="#547c77"
        scale={0.38}
        opacity={0.4}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
      <FlowMarkerInstances
        markers={elevatedRiskMarkers}
        color="#b66f63"
        scale={0.46}
        opacity={0.4}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
      <FlowMarkerInstances
        markers={criticalRiskMarkers}
        color="#d9473a"
        scale={0.7}
        opacity={0.94}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
    </group>
  );
}

function FlowMarkerInstances({
  markers,
  color,
  scale,
  opacity,
  reducedMotion,
  paused,
  onSelectWaybill,
}: {
  markers: readonly FlowMarker[];
  color: string;
  scale: number;
  opacity: number;
  reducedMotion: boolean;
  paused: boolean;
  onSelectWaybill: (waybillID: WaybillID) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const elapsed = useRef(0);
  const { gl } = useThree();

  const updateMarkers = useCallback(
    (time: number) => {
      if (mesh.current === null) {
        return;
      }
      markers.forEach((marker, index) => {
        const progress = (marker.phase + time * marker.speed) % 1;
        const point = marker.route.curve.getPointAt(progress, markerPoint);
        markerTransform.makeScale(scale, scale, scale);
        markerTransform.setPosition(point);
        mesh.current?.setMatrixAt(index, markerTransform);
      });
      mesh.current.instanceMatrix.needsUpdate = true;
    },
    [markers, scale],
  );

  useLayoutEffect(() => {
    updateMarkers(0);
  }, [updateMarkers]);

  useFrame((_, delta) => {
    if (reducedMotion || paused) {
      return;
    }
    elapsed.current += Math.min(delta, 0.05);
    updateMarkers(elapsed.current);
  });

  const setCursor = useCallback(
    (cursor: "default" | "pointer") => {
      gl.domElement.style.cursor = cursor;
    },
    [gl],
  );

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, markers.length]}
      frustumCulled={false}
      onClick={(event) => {
        event.stopPropagation();
        if (event.instanceId === undefined) {
          return;
        }
        const marker = markers[event.instanceId];
        if (marker?.route.anomaly !== undefined) {
          onSelectWaybill(marker.route.anomaly.waybill_id);
        }
      }}
      onPointerOver={(event) => {
        if (
          event.instanceId !== undefined &&
          markers[event.instanceId]?.route.anomaly !== undefined
        ) {
          event.stopPropagation();
          setCursor("pointer");
        }
      }}
      onPointerOut={() => setCursor("default")}
    >
      <sphereGeometry args={[0.072, 8, 6]} />
      <meshBasicMaterial
        color={color}
        transparent={opacity < 1}
        opacity={opacity}
        toneMapped={false}
      />
    </instancedMesh>
  );
}

function RiskRings({
  hubs,
  reducedMotion,
  paused,
}: {
  hubs: readonly SceneHub[];
  reducedMotion: boolean;
  paused: boolean;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const elapsed = useRef(0);
  const emphasizedHubs = useMemo(
    () => hubs.slice(0, maxEmphasizedRiskHubs),
    [hubs],
  );

  const updateRings = useCallback(
    (time: number) => {
      if (mesh.current === null) {
        return;
      }
      emphasizedHubs.forEach((hub, index) => {
        const pulse = reducedMotion
          ? 1
          : 0.94 + ((Math.sin(time * 2 + index * 0.62) + 1) / 2) * 0.16;
        transform.position.set(hub.position.x, -0.11, hub.position.z);
        transform.rotation.set(Math.PI / 2, 0, 0);
        transform.scale.setScalar(
          pulse * hub.scale * (0.68 + hub.signalHeight * 0.55),
        );
        transform.updateMatrix();
        mesh.current?.setMatrixAt(index, transform.matrix);
      });
      mesh.current.instanceMatrix.needsUpdate = true;
    },
    [emphasizedHubs, reducedMotion],
  );

  useLayoutEffect(() => {
    updateRings(0);
  }, [updateRings]);

  useFrame((_, delta) => {
    if (reducedMotion || paused) {
      return;
    }
    elapsed.current += Math.min(delta, 0.05);
    updateRings(elapsed.current);
  });

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, emphasizedHubs.length]}
      frustumCulled={false}
    >
      <torusGeometry args={[0.2, 0.012, 6, 20]} />
      <meshBasicMaterial color="#d54a3d" transparent opacity={0.52} />
    </instancedMesh>
  );
}

export type FacilityCameraTarget = {
  x: number;
  y: number;
  z: number;
  zoom: number;
  tracking: boolean;
};

export function resolveFacilityCameraTarget(
  detail: FacilityLayout,
  preset: FacilityCameraPreset,
  selection: FacilitySceneSelection | null,
  elapsed: number,
): FacilityCameraTarget {
  if (preset === "follow") {
    const selectedVehicle =
      selection?.kind === "vehicle"
        ? detail.vehicles.find((vehicle) => vehicle.id === selection.id)
        : undefined;
    const vehicle =
      selectedVehicle ??
      detail.vehicles.find((item) => item.state === "alert") ??
      detail.vehicles.find((item) => item.state === "moving") ??
      detail.vehicles[0];
    if (vehicle !== undefined) {
      const sample = sampleFacilityVehiclePath(
        detail.vehiclePath,
        vehicle.phase + elapsed * vehicle.speed,
      );
      return {
        x: sample.x,
        y: -0.06,
        z: sample.z,
        zoom: 4.2,
        tracking: vehicle.speed > 0,
      };
    }
  }

  if (preset === "risk") {
    if (selection?.kind === "alert") {
      const beacon = detail.elements.find(
        (element) => element.kind === "beacon",
      );
      if (beacon !== undefined) {
        return {
          x: beacon.x,
          y: -0.04,
          z: beacon.z,
          zoom: 3.8,
          tracking: false,
        };
      }
    }
    const selectedSegment =
      selection?.kind === "route"
        ? detail.transportSegments.find(
            (segment) => segment.id === selection.id,
          )
        : undefined;
    const segment =
      selectedSegment ??
      detail.transportSegments.find((item) => item.status === "risk");
    if (segment !== undefined) {
      return {
        x: (segment.from[0] + segment.to[0]) / 2,
        y: -0.08,
        z: (segment.from[1] + segment.to[1]) / 2,
        zoom: 4.2,
        tracking: false,
      };
    }
  }

  return {
    x: 0,
    y: -0.08,
    z: 0,
    zoom: 3.05,
    tracking: false,
  };
}

function CameraRig({
  networkHubs,
  selectedHub,
  detail,
  preset,
  zoomScale,
  selection,
  reducedMotion,
  paused,
}: {
  networkHubs: readonly SceneHub[];
  selectedHub: SceneHub | undefined;
  detail: FacilityLayout | undefined;
  preset: FacilityCameraPreset;
  zoomScale: number;
  selection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
}) {
  const { camera, gl, size, invalidate } = useThree();
  const animating = useRef(true);
  const elapsed = useRef(0);
  const currentFocus = useRef(new Vector3());
  const desiredFocus = useRef(new Vector3());
  const desiredPosition = useRef(new Vector3(...networkCameraOffset));
  const desiredZoom = useRef(1);
  const networkFocus = useMemo(() => {
    if (networkHubs.length === 0) {
      return new Vector3();
    }
    const xValues = networkHubs.map((hub) => hub.position.x);
    const zValues = networkHubs.map((hub) => hub.position.z);
    const boundsCenterX =
      (Math.min(...xValues) + Math.max(...xValues)) / 2;
    const boundsCenterZ =
      (Math.min(...zValues) + Math.max(...zValues)) / 2;
    const centroidX =
      xValues.reduce((sum, value) => sum + value, 0) / xValues.length;
    const centroidZ =
      zValues.reduce((sum, value) => sum + value, 0) / zValues.length;
    return new Vector3(
      MathUtils.lerp(boundsCenterX, centroidX, 0.5),
      0,
      MathUtils.lerp(boundsCenterZ, centroidZ, 0.5),
    );
  }, [networkHubs]);

  const updateDesiredView = useCallback(
    (time: number) => {
      const baseZoom = Math.min(size.width / 23, size.height / 14);
      if (selectedHub === undefined || detail === undefined) {
        desiredFocus.current.copy(networkFocus);
        desiredPosition.current
          .set(...networkCameraOffset)
          .add(networkFocus);
        desiredZoom.current =
          baseZoom * networkZoomMultiplier * zoomScale;
      } else {
        const target = resolveFacilityCameraTarget(
          detail,
          preset,
          selection,
          time,
        );
        desiredFocus.current
          .set(target.x, target.y, target.z)
          .applyAxisAngle(sceneUp, selectedHub.rotationY)
          .multiplyScalar(selectedHub.scale * detailFacilityScale)
          .add(selectedHub.position);
        const offset =
          preset === "overview"
            ? overviewCameraOffset
            : preset === "follow"
              ? followCameraOffset
              : riskCameraOffset;
        desiredPosition.current.copy(desiredFocus.current).add(offset);
        desiredZoom.current = baseZoom * target.zoom * zoomScale;
      }
      if (import.meta.env.DEV) {
        gl.domElement.dataset.cameraPreset = preset;
        gl.domElement.dataset.cameraFocus = [
          desiredFocus.current.x,
          desiredFocus.current.y,
          desiredFocus.current.z,
        ]
          .map((value) => value.toFixed(4))
          .join(",");
        gl.domElement.dataset.cameraZoom = desiredZoom.current.toFixed(2);
      }
    },
    [
      detail,
      gl,
      networkFocus,
      preset,
      selectedHub,
      selection,
      size.height,
      size.width,
      zoomScale,
    ],
  );

  useEffect(() => {
    elapsed.current = 0;
  }, [detail, selectedHub]);

  useEffect(() => {
    if (!(camera instanceof OrthographicCamera)) {
      return;
    }
    updateDesiredView(elapsed.current);
    animating.current = !reducedMotion;
    if (reducedMotion) {
      currentFocus.current.copy(desiredFocus.current);
      camera.position.copy(desiredPosition.current);
      camera.zoom = desiredZoom.current;
      camera.lookAt(currentFocus.current);
      camera.updateProjectionMatrix();
    }
    invalidate();
  }, [
    camera,
    invalidate,
    reducedMotion,
    updateDesiredView,
  ]);

  useFrame((_, delta) => {
    if (paused || !(camera instanceof OrthographicCamera)) {
      return;
    }
    if (!reducedMotion && detail !== undefined) {
      elapsed.current += Math.min(delta, 0.05);
    }
    const tracking = detail !== undefined && preset === "follow";
    if (tracking) {
      updateDesiredView(elapsed.current);
    }
    if (reducedMotion || (!animating.current && !tracking)) {
      return;
    }
    const alpha = 1 - Math.exp(-Math.min(delta, 0.05) * 4.6);
    camera.position.lerp(desiredPosition.current, alpha);
    camera.zoom = MathUtils.lerp(camera.zoom, desiredZoom.current, alpha);
    currentFocus.current.lerp(desiredFocus.current, alpha);
    camera.lookAt(currentFocus.current);
    camera.updateProjectionMatrix();
    if (
      !tracking &&
      camera.position.distanceToSquared(desiredPosition.current) < 0.0005 &&
      Math.abs(camera.zoom - desiredZoom.current) < 0.01
    ) {
      camera.position.copy(desiredPosition.current);
      camera.zoom = desiredZoom.current;
      currentFocus.current.copy(desiredFocus.current);
      camera.lookAt(currentFocus.current);
      camera.updateProjectionMatrix();
      animating.current = false;
    }
  });

  return null;
}

function SceneReporter({
  reportKey,
  onReady,
  onDrawCalls,
}: {
  reportKey: string;
  onReady: () => void;
  onDrawCalls: (drawCalls: number) => void;
}) {
  const { gl, invalidate } = useThree();
  const frame = useRef(0);

  useEffect(() => {
    frame.current = 0;
    invalidate();
  }, [invalidate, reportKey]);

  useFrame(() => {
    frame.current += 1;
    if (import.meta.env.DEV) {
      gl.domElement.dataset.sceneFrame = String(frame.current);
    }
    if (frame.current === 1) {
      onReady();
      invalidate();
    }
    if (frame.current === 2 || frame.current === 8) {
      onDrawCalls(gl.info.render.calls);
    }
  });

  return null;
}

function WebGLContextObserver({ onFailure }: { onFailure: () => void }) {
  const { gl } = useThree();

  useEffect(() => {
    const canvas = gl.domElement;
    const handleContextLoss = (event: Event) => {
      event.preventDefault();
      onFailure();
    };
    canvas.addEventListener("webglcontextlost", handleContextLoss, {
      once: true,
    });
    return () => {
      canvas.removeEventListener("webglcontextlost", handleContextLoss);
    };
  }, [gl, onFailure]);

  return null;
}

export function buildSceneModel(
  hubs: readonly HubOverview[],
  routes: readonly RouteOverview[],
  anomalies: readonly AnomalyOverview[],
): SceneModel {
  const maxInFlight = Math.max(...hubs.map((hub) => hub.in_flight), 1);
  const maxAnomalies = Math.max(...hubs.map((hub) => hub.anomalies), 1);
  const capacities = hubs.map((hub) => hub.daily_capacity);
  const minimumCapacity =
    capacities.length === 0 ? 0 : Math.min(...capacities);
  const maximumCapacity =
    capacities.length === 0 ? 1 : Math.max(...capacities);
  const capacitySpan = Math.max(maximumCapacity - minimumCapacity, 1);
  const capacityBreaks = buildCapacityBreaks(hubs);
  const positions = new Map(
    hubs.map((hub) => [hub.hub_id, projectHub(hub)]),
  );
  const rotations = buildHubRotations(hubs, routes, positions);
  const sceneHubs = hubs.map((hub) => {
    const priority = hub.anomalies >= 2;
    const activity = hub.in_flight / maxInFlight;
    const capacityLoad =
      (hub.daily_capacity - minimumCapacity) / capacitySpan;
    return {
      hub,
      position: positions.get(hub.hub_id) ?? projectHub(hub),
      archetype: classifyHub(hub.daily_capacity, capacityBreaks),
      scale: 0.82 + capacityLoad * 0.28,
      activity,
      cargoSlots:
        hub.in_flight === 0 ? 0 : Math.max(1, Math.ceil(activity * 3)),
      signalHeight:
        hub.anomalies === 0
          ? 0
          : 0.14 + (hub.anomalies / maxAnomalies) * 0.22,
      rotationY: rotations.get(hub.hub_id) ?? 0,
      priority,
    };
  });
  const hubsByID = new Map(sceneHubs.map((item) => [item.hub.hub_id, item]));
  const anomalyByRoute = new Map<string, AnomalyOverview>();
  for (const item of anomalies) {
    if (item.route_id === undefined) {
      continue;
    }
    const current = anomalyByRoute.get(item.route_id);
    if (current === undefined || item.risk_score > current.risk_score) {
      anomalyByRoute.set(item.route_id, item);
    }
  }
  const sceneRoutes = routes.flatMap((route) => {
    const origin = hubsByID.get(route.origin_hub_id);
    const destination = hubsByID.get(route.destination_hub_id);
    if (origin === undefined || destination === undefined) {
      return [];
    }
    const from = origin.position.clone().setY(0.08);
    const to = destination.position.clone().setY(0.08);
    const distance = from.distanceTo(to);
    const midpointHeight = route.anomalies > 0
      ? 0.7 + Math.min(distance * 0.09, 1.8)
      : 0.3 + Math.min(distance * 0.04, 0.8);
    const midpoint = from
      .clone()
      .add(to)
      .multiplyScalar(0.5)
      .setY(midpointHeight);
    return [{
      route,
      curve: new QuadraticBezierCurve3(from, midpoint, to),
      anomaly: anomalyByRoute.get(route.route_id),
    }];
  });
  const markers = sceneRoutes.flatMap((route, routeIndex) => {
    const markerCount = Math.min(
      route.route.waybills,
      maxFlowMarkersPerRoute,
    );
    return Array.from({ length: markerCount }, (_, markerIndex) => ({
      route,
      phase:
        ((routeIndex * 0.61803398875 +
          markerIndex / Math.max(markerCount, 1)) %
          1 +
          1) %
        1,
      speed: 0.025 + ((routeIndex * 7 + markerIndex * 3) % 11) * 0.002,
    }));
  });

  return {
    hubs: sceneHubs,
    hubsByID,
    facilityParts: sceneHubs.flatMap(createFacilityParts),
    facilityLayouts: buildFacilityLayouts(hubs, routes),
    routes: sceneRoutes,
    markers,
    riskHubs: sceneHubs
      .filter((item) => item.priority)
      .sort((left, right) => right.hub.anomalies - left.hub.anomalies)
      .slice(0, 12),
  };
}

export function buildSceneView(
  model: SceneModel,
  selectedHubID: string | null,
): SceneView {
  const selectedHub =
    selectedHubID === null ? undefined : model.hubsByID.get(selectedHubID);
  const detail =
    selectedHubID === null
      ? undefined
      : model.facilityLayouts.get(selectedHubID);
  if (selectedHub === undefined || detail === undefined) {
    return {
      mode: "network",
      selectedHub: undefined,
      detail: undefined,
      hubs: model.hubs,
      facilityParts: model.facilityParts,
      routes: model.routes,
      markers: model.markers,
      riskHubs: model.riskHubs,
    };
  }

  return {
    mode: "facility",
    selectedHub,
    detail,
    hubs: [selectedHub],
    facilityParts: [],
    routes: [],
    markers: [],
    riskHubs: [],
  };
}

function buildCapacityBreaks(
  hubs: readonly HubOverview[],
): CapacityBreaks {
  const capacities = hubs
    .map((hub) => hub.daily_capacity)
    .sort((left, right) => left - right);
  return {
    lower: quantile(capacities, 0.25),
    middle: quantile(capacities, 0.5),
    upper: quantile(capacities, 0.75),
  };
}

function quantile(values: readonly number[], fraction: number): number {
  const index = Math.max(0, Math.ceil(values.length * fraction) - 1);
  return values[index] ?? 0;
}

function classifyHub(
  dailyCapacity: number,
  breaks: CapacityBreaks,
): HubArchetype {
  if (dailyCapacity <= breaks.lower) {
    return "local-depot";
  }
  if (dailyCapacity <= breaks.middle) {
    return "regional-cross-dock";
  }
  if (dailyCapacity <= breaks.upper) {
    return "gateway-campus";
  }
  return "yard-terminal";
}

function createFacilityParts(sceneHub: SceneHub): HubPart[] {
  const fixedParts = facilityTemplates[sceneHub.archetype].map((template) => ({
    ...template,
    sceneHub,
    color: facilityPartColorFor(template.kind, sceneHub.archetype),
  }));
  const cargoParts = cargoPositions[sceneHub.archetype]
    .slice(0, sceneHub.cargoSlots)
    .map(([x, z]) => ({
      kind: "cargo",
      offset: [x, -0.105, z],
      size: [0.055, 0.05, 0.035],
      sceneHub,
      color: sceneHub.activity > 0.66 ? "#c58d2d" : "#b79a55",
    }) satisfies HubPart);
  const signalPosition = signalPositions[sceneHub.archetype];
  const signalParts: HubPart[] =
    sceneHub.signalHeight === 0
      ? []
      : [
          {
            kind: "signal",
            offset: [
              signalPosition[0],
              -0.135 + sceneHub.signalHeight / 2,
              signalPosition[1],
            ],
            size: [0.026, sceneHub.signalHeight, 0.026],
            sceneHub,
            color: "#df3f30",
          },
        ];

  return [...fixedParts, ...cargoParts, ...signalParts];
}

function facilityPartColorFor(
  kind: HubPartTemplate["kind"],
  archetype: HubArchetype,
): string {
  switch (kind) {
    case "pad":
      return "#d9dfdc";
    case "structure":
      return structureColors[archetype];
    case "roof":
      return "#eef1ee";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function countArchetypes(
  hubs: readonly SceneHub[],
): Record<HubArchetype, number> {
  const counts: Record<HubArchetype, number> = {
    "local-depot": 0,
    "regional-cross-dock": 0,
    "gateway-campus": 0,
    "yard-terminal": 0,
  };
  for (const hub of hubs) {
    counts[hub.archetype] += 1;
  }
  return counts;
}

function buildHubRotations(
  hubs: readonly HubOverview[],
  routes: readonly RouteOverview[],
  positions: ReadonlyMap<string, Vector3>,
): ReadonlyMap<string, number> {
  const primaryRoutes = new Map<
    string,
    { route: RouteOverview; targetHubID: string }
  >();
  for (const route of routes) {
    keepPrimaryRoute(primaryRoutes, route.origin_hub_id, {
      route,
      targetHubID: route.destination_hub_id,
    });
    keepPrimaryRoute(primaryRoutes, route.destination_hub_id, {
      route,
      targetHubID: route.origin_hub_id,
    });
  }

  return new Map(
    hubs.map((hub) => {
      const origin = positions.get(hub.hub_id);
      const connection = primaryRoutes.get(hub.hub_id);
      const target =
        connection === undefined
          ? undefined
          : positions.get(connection.targetHubID);
      if (origin === undefined || target === undefined) {
        return [hub.hub_id, fallbackHubRotation(hub)];
      }
      const direction = target.clone().sub(origin);
      return [hub.hub_id, Math.atan2(-direction.z, direction.x)];
    }),
  );
}

function keepPrimaryRoute(
  primaryRoutes: Map<
    string,
    { route: RouteOverview; targetHubID: string }
  >,
  hubID: string,
  candidate: { route: RouteOverview; targetHubID: string },
) {
  const current = primaryRoutes.get(hubID);
  if (
    current === undefined ||
    routePrecedes(candidate.route, current.route)
  ) {
    primaryRoutes.set(hubID, candidate);
  }
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

function fallbackHubRotation(hub: HubOverview): number {
  return MathUtils.degToRad(((hub.longitude + hub.latitude) % 90) - 45);
}

function createRouteGeometry(routes: readonly SceneRoute[]): BufferGeometry {
  const positions: number[] = [];
  for (const route of routes) {
    const points = route.curve.getPoints(20);
    for (let index = 1; index < points.length; index += 1) {
      const previous = points[index - 1];
      const current = points[index];
      if (previous === undefined || current === undefined) {
        continue;
      }
      positions.push(
        previous.x,
        previous.y,
        previous.z,
        current.x,
        current.y,
        current.z,
      );
    }
  }
  const geometry = new BufferGeometry();
  geometry.setAttribute("position", new Float32BufferAttribute(positions, 3));
  geometry.computeBoundingSphere();
  return geometry;
}

function projectHub(hub: HubOverview): Vector3 {
  const x =
    ((hub.longitude - coordinateBounds.minLongitude) /
      (coordinateBounds.maxLongitude - coordinateBounds.minLongitude) -
      0.5) *
    sceneWidth;
  const z =
    -(
      (hub.latitude - coordinateBounds.minLatitude) /
        (coordinateBounds.maxLatitude - coordinateBounds.minLatitude) -
      0.5
    ) * sceneDepth;
  return new Vector3(x, 0, z);
}
