import { Canvas, useFrame, useThree, type ThreeEvent } from "@react-three/fiber";
import {
  BufferGeometry,
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

type HubNetworkSceneProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
  selectedHubID: string | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelectHub: (hubID: string) => void;
  onSelectWaybill: (waybillID: WaybillID) => void;
  onFailure: () => void;
  onReady: () => void;
  onDrawCalls: (drawCalls: number) => void;
  onStats: (stats: SceneStats) => void;
};

export type SceneStats = {
  hubs: number;
  routes: number;
  markers: number;
};

type SceneHub = {
  hub: HubOverview;
  position: Vector3;
  height: number;
  priority: boolean;
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

const coordinateBounds = {
  minLongitude: 73,
  maxLongitude: 135,
  minLatitude: 18,
  maxLatitude: 54,
};

const sceneWidth = 22;
const sceneDepth = 12;
const maxFlowMarkersPerRoute = 4;
const transform = new Object3D();
const markerTransform = new Matrix4();
const markerPoint = new Vector3();

export default function HubNetworkScene({
  hubs,
  routes,
  anomalies,
  selectedHubID,
  reducedMotion,
  paused,
  onSelectHub,
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
  const selectedHub = selectedHubID === null
    ? undefined
    : model.hubsByID.get(selectedHubID);

  useEffect(() => {
    onStats({
      hubs: model.hubs.length,
      routes: model.routes.length,
      markers: model.markers.length,
    });
  }, [model, onStats]);

  return (
    <Canvas
      orthographic
      camera={{ position: [11, 18, 15], near: 0.1, far: 100, zoom: 34 }}
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

      <StrategyTable />
      <RouteLines routes={model.routes} />
      <HubColumns
        hubs={model.hubs}
        selectedHubID={selectedHubID}
        onSelectHub={onSelectHub}
      />
      <FlowMarkers
        markers={model.markers}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
      <RiskRings
        hubs={model.riskHubs}
        reducedMotion={reducedMotion}
        paused={paused}
      />
      {selectedHub !== undefined && <SelectionBeacon hub={selectedHub} />}
      <CameraRig
        selectedHub={selectedHub}
        reducedMotion={reducedMotion}
        paused={paused}
      />
      <SceneReporter onReady={onReady} onDrawCalls={onDrawCalls} />
    </Canvas>
  );
}

function StrategyTable() {
  return (
    <group>
      <mesh position={[0, -0.32, 0]}>
        <boxGeometry args={[26, 0.2, 16]} />
        <meshStandardMaterial color="#e9ece8" roughness={1} />
      </mesh>
      <gridHelper
        args={[26, 26, "#9caaa7", "#d2d8d4"]}
        position={[0, -0.19, 0]}
        scale={[1, 1, 0.615]}
      />
    </group>
  );
}

function HubColumns({
  hubs,
  selectedHubID,
  onSelectHub,
}: {
  hubs: readonly SceneHub[];
  selectedHubID: string | null;
  onSelectHub: (hubID: string) => void;
}) {
  const normalHubs = hubs.filter((item) => !item.priority);
  const riskHubs = hubs.filter((item) => item.priority);

  return (
    <group>
      <HubInstances
        hubs={normalHubs}
        selectedHubID={selectedHubID}
        color="#285f5b"
        emphasis={false}
        onSelectHub={onSelectHub}
      />
      <HubInstances
        hubs={riskHubs}
        selectedHubID={selectedHubID}
        color="#df3f30"
        emphasis
        onSelectHub={onSelectHub}
      />
    </group>
  );
}

function HubInstances({
  hubs,
  selectedHubID,
  color,
  emphasis,
  onSelectHub,
}: {
  hubs: readonly SceneHub[];
  selectedHubID: string | null;
  color: string;
  emphasis: boolean;
  onSelectHub: (hubID: string) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const { gl } = useThree();

  useLayoutEffect(() => {
    if (mesh.current === null) {
      return;
    }
    hubs.forEach((item, index) => {
      transform.position.set(
        item.position.x,
        item.height / 2 - 0.18,
        item.position.z,
      );
      transform.rotation.set(0, 0, 0);
      transform.scale.set(
        item.hub.hub_id === selectedHubID ? 1.45 : 1,
        1,
        item.hub.hub_id === selectedHubID ? 1.45 : 1,
      );
      transform.updateMatrix();
      mesh.current?.setMatrixAt(index, transform.matrix);
    });
    mesh.current.instanceMatrix.needsUpdate = true;
  }, [hubs, selectedHubID]);

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
        const item = hubs[event.instanceId];
        if (item !== undefined) {
          onSelectHub(item.hub.hub_id);
        }
      }
    },
    [hubs, onSelectHub],
  );

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, hubs.length]}
      frustumCulled={false}
      onClick={select}
      onPointerOver={(event) => {
        event.stopPropagation();
        setCursor("pointer");
      }}
      onPointerOut={() => setCursor("default")}
    >
      <cylinderGeometry
        args={emphasis ? [0.07, 0.14, 1, 6] : [0.045, 0.08, 1, 8]}
      />
      <meshStandardMaterial
        color={color}
        roughness={0.68}
        metalness={0.02}
      />
    </instancedMesh>
  );
}

function RouteLines({ routes }: { routes: readonly SceneRoute[] }) {
  const normalGeometry = useMemo(
    () => createRouteGeometry(routes.filter((item) => item.route.anomalies === 0)),
    [routes],
  );
  const riskGeometry = useMemo(
    () => createRouteGeometry(routes.filter((item) => item.route.anomalies > 0)),
    [routes],
  );

  useEffect(
    () => () => {
      normalGeometry.dispose();
      riskGeometry.dispose();
    },
    [normalGeometry, riskGeometry],
  );

  return (
    <group>
      <lineSegments geometry={normalGeometry}>
        <lineBasicMaterial
          color="#5e6e6a"
          transparent
          opacity={0.2}
          toneMapped={false}
        />
      </lineSegments>
      <lineSegments geometry={riskGeometry}>
        <lineBasicMaterial
          color="#df3f30"
          transparent
          opacity={0.88}
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
  const riskMarkers = markers.filter(
    (marker) => marker.route.anomaly !== undefined,
  );

  return (
    <group>
      <FlowMarkerInstances
        markers={normalMarkers}
        color="#176e68"
        scale={0.72}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
      <FlowMarkerInstances
        markers={riskMarkers}
        color="#ef3f2f"
        scale={1.15}
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
  reducedMotion,
  paused,
  onSelectWaybill,
}: {
  markers: readonly FlowMarker[];
  color: string;
  scale: number;
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
      <meshBasicMaterial color={color} toneMapped={false} />
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

  const updateRings = useCallback(
    (time: number) => {
      if (mesh.current === null) {
        return;
      }
      hubs.forEach((hub, index) => {
        const pulse = reducedMotion
          ? 1
          : 0.88 + ((Math.sin(time * 2.4 + index * 0.62) + 1) / 2) * 0.5;
        transform.position.set(hub.position.x, -0.11, hub.position.z);
        transform.rotation.set(Math.PI / 2, 0, 0);
        transform.scale.setScalar(pulse);
        transform.updateMatrix();
        mesh.current?.setMatrixAt(index, transform.matrix);
      });
      mesh.current.instanceMatrix.needsUpdate = true;
    },
    [hubs, reducedMotion],
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
      args={[undefined, undefined, hubs.length]}
      frustumCulled={false}
    >
      <torusGeometry args={[0.22, 0.018, 6, 20]} />
      <meshBasicMaterial color="#e13f30" transparent opacity={0.76} />
    </instancedMesh>
  );
}

function SelectionBeacon({ hub }: { hub: SceneHub }) {
  return (
    <group position={[hub.position.x, 0, hub.position.z]}>
      <mesh position={[0, -0.08, 0]} rotation={[-Math.PI / 2, 0, 0]}>
        <ringGeometry args={[0.32, 0.39, 32]} />
        <meshBasicMaterial color="#171a1a" transparent opacity={0.9} />
      </mesh>
      <mesh position={[0, 1.55, 0]}>
        <octahedronGeometry args={[0.16, 0]} />
        <meshStandardMaterial color="#171a1a" roughness={0.5} />
      </mesh>
      <mesh position={[0, 0.78, 0]}>
        <cylinderGeometry args={[0.012, 0.012, 1.35, 5]} />
        <meshBasicMaterial color="#171a1a" transparent opacity={0.46} />
      </mesh>
    </group>
  );
}

function CameraRig({
  selectedHub,
  reducedMotion,
  paused,
}: {
  selectedHub: SceneHub | undefined;
  reducedMotion: boolean;
  paused: boolean;
}) {
  const { camera, size, invalidate } = useThree();
  const animating = useRef(true);
  const focus = useMemo(
    () =>
      selectedHub === undefined
        ? new Vector3(0, 0, 0)
        : new Vector3(selectedHub.position.x, 0.4, selectedHub.position.z),
    [selectedHub],
  );
  const desiredPosition = useMemo(
    () =>
      selectedHub === undefined
        ? new Vector3(11, 18, 15)
        : focus.clone().add(new Vector3(6.8, 10.5, 8.4)),
    [focus, selectedHub],
  );
  const baseZoom = Math.min(size.width / 23, size.height / 14);
  const desiredZoom = baseZoom * (selectedHub === undefined ? 1 : 1.48);

  useEffect(() => {
    if (!(camera instanceof OrthographicCamera)) {
      return;
    }
    animating.current = !reducedMotion;
    if (reducedMotion) {
      camera.position.copy(desiredPosition);
      camera.zoom = desiredZoom;
      camera.lookAt(focus);
      camera.updateProjectionMatrix();
      invalidate();
    }
  }, [camera, desiredPosition, desiredZoom, focus, invalidate, reducedMotion]);

  useFrame((_, delta) => {
    if (
      paused ||
      !animating.current ||
      !(camera instanceof OrthographicCamera)
    ) {
      return;
    }
    const alpha = 1 - Math.exp(-Math.min(delta, 0.05) * 4.6);
    camera.position.lerp(desiredPosition, alpha);
    camera.zoom = MathUtils.lerp(camera.zoom, desiredZoom, alpha);
    camera.lookAt(focus);
    camera.updateProjectionMatrix();
    if (
      camera.position.distanceToSquared(desiredPosition) < 0.0005 &&
      Math.abs(camera.zoom - desiredZoom) < 0.01
    ) {
      camera.position.copy(desiredPosition);
      camera.zoom = desiredZoom;
      camera.updateProjectionMatrix();
      animating.current = false;
    }
  });

  return null;
}

function SceneReporter({
  onReady,
  onDrawCalls,
}: {
  onReady: () => void;
  onDrawCalls: (drawCalls: number) => void;
}) {
  const { gl, invalidate } = useThree();
  const frame = useRef(0);

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
) {
  const maxInFlight = Math.max(...hubs.map((hub) => hub.in_flight), 1);
  const sceneHubs = hubs.map((hub) => {
    const priority = hub.anomalies >= 2;
    const load = hub.in_flight / maxInFlight;
    return {
      hub,
      position: projectHub(hub),
      height: priority ? 0.34 + load * 1.08 : 0.09 + load * 0.3,
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
    routes: sceneRoutes,
    markers,
    riskHubs: sceneHubs
      .filter((item) => item.priority)
      .sort((left, right) => right.hub.anomalies - left.hub.anomalies)
      .slice(0, 12),
  };
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
