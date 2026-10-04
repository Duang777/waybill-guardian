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
  onReady: () => void;
  onDrawCalls: (drawCalls: number) => void;
};

type SceneHub = {
  hub: HubOverview;
  position: Vector3;
  height: number;
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
const transform = new Object3D();
const markerTransform = new Matrix4();

export default function HubNetworkScene({
  hubs,
  routes,
  anomalies,
  selectedHubID,
  reducedMotion,
  paused,
  onSelectHub,
  onSelectWaybill,
  onReady,
  onDrawCalls,
}: HubNetworkSceneProps) {
  const model = useMemo(
    () => buildSceneModel(hubs, routes, anomalies),
    [anomalies, hubs, routes],
  );
  const selectedHub = selectedHubID === null
    ? undefined
    : model.hubsByID.get(selectedHubID);

  return (
    <Canvas
      orthographic
      camera={{ position: [14, 15, 18], near: 0.1, far: 100, zoom: 32 }}
      dpr={[1, 1.5]}
      frameloop={reducedMotion || paused ? "demand" : "always"}
      gl={{
        alpha: false,
        antialias: true,
        powerPreference: "high-performance",
      }}
      onCreated={({ gl }) => {
        gl.setClearColor("#f8fbfa", 1);
      }}
    >
      <color attach="background" args={["#f8fbfa"]} />
      <ambientLight intensity={2.2} />
      <hemisphereLight args={["#ffffff", "#b9cbc8", 1.5]} />
      <directionalLight position={[8, 14, 10]} intensity={2.4} />

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
        <boxGeometry args={[26, 0.24, 16]} />
        <meshStandardMaterial color="#f0f5f4" roughness={0.96} />
      </mesh>
      <gridHelper
        args={[26, 26, "#b8d0cc", "#dce8e6"]}
        position={[0, -0.19, 0]}
        scale={[1, 1, 0.615]}
      />
      <mesh position={[-6.7, -0.12, 2.4]}>
        <boxGeometry args={[6.2, 0.14, 3.6]} />
        <meshStandardMaterial color="#e9f1ef" roughness={0.9} />
      </mesh>
      <mesh position={[4.1, -0.1, -2.2]}>
        <boxGeometry args={[8.8, 0.18, 4.1]} />
        <meshStandardMaterial color="#edf4f2" roughness={0.9} />
      </mesh>
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
  const normalHubs = hubs.filter((item) => item.hub.anomalies === 0);
  const riskHubs = hubs.filter((item) => item.hub.anomalies > 0);

  return (
    <group>
      <HubInstances
        hubs={normalHubs}
        selectedHubID={selectedHubID}
        color="#4f9891"
        onSelectHub={onSelectHub}
      />
      <HubInstances
        hubs={riskHubs}
        selectedHubID={selectedHubID}
        color="#df604f"
        onSelectHub={onSelectHub}
      />
    </group>
  );
}

function HubInstances({
  hubs,
  selectedHubID,
  color,
  onSelectHub,
}: {
  hubs: readonly SceneHub[];
  selectedHubID: string | null;
  color: string;
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
      <cylinderGeometry args={[0.09, 0.16, 1, 6]} />
      <meshStandardMaterial
        color={color}
        roughness={0.5}
        metalness={0.05}
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
          color="#73aaa5"
          transparent
          opacity={0.46}
          toneMapped={false}
        />
      </lineSegments>
      <lineSegments geometry={riskGeometry}>
        <lineBasicMaterial
          color="#d75b4b"
          transparent
          opacity={0.78}
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
        color="#2f8f88"
        scale={1}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelectWaybill={onSelectWaybill}
      />
      <FlowMarkerInstances
        markers={riskMarkers}
        color="#ee6654"
        scale={1.28}
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
        const point = marker.route.curve.getPointAt(progress);
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
      <torusGeometry args={[0.24, 0.025, 6, 20]} />
      <meshBasicMaterial color="#e26352" transparent opacity={0.68} />
    </instancedMesh>
  );
}

function SelectionBeacon({ hub }: { hub: SceneHub }) {
  return (
    <group position={[hub.position.x, 0, hub.position.z]}>
      <mesh position={[0, -0.08, 0]} rotation={[-Math.PI / 2, 0, 0]}>
        <ringGeometry args={[0.32, 0.39, 32]} />
        <meshBasicMaterial color="#146d68" transparent opacity={0.9} />
      </mesh>
      <mesh position={[0, 1.55, 0]}>
        <octahedronGeometry args={[0.16, 0]} />
        <meshStandardMaterial color="#146d68" roughness={0.35} />
      </mesh>
      <mesh position={[0, 0.78, 0]}>
        <cylinderGeometry args={[0.012, 0.012, 1.35, 5]} />
        <meshBasicMaterial color="#146d68" transparent opacity={0.5} />
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
        ? new Vector3(14, 15, 18)
        : focus.clone().add(new Vector3(7.2, 8.2, 9.2)),
    [focus, selectedHub],
  );
  const baseZoom = Math.min(size.width / 23.5, size.height / 14.5);
  const desiredZoom = baseZoom * (selectedHub === undefined ? 1 : 1.42);

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

function buildSceneModel(
  hubs: readonly HubOverview[],
  routes: readonly RouteOverview[],
  anomalies: readonly AnomalyOverview[],
) {
  const maxInFlight = Math.max(...hubs.map((hub) => hub.in_flight), 1);
  const sceneHubs = hubs.map((hub) => ({
    hub,
    position: projectHub(hub),
    height: 0.28 + (hub.in_flight / maxInFlight) * 0.82,
  }));
  const hubsByID = new Map(sceneHubs.map((item) => [item.hub.hub_id, item]));
  const anomalyByRoute = new Map(
    anomalies.flatMap((item) =>
      item.route_id === undefined ? [] : [[item.route_id, item] as const],
    ),
  );
  const sceneRoutes = routes.flatMap((route) => {
    const origin = hubsByID.get(route.origin_hub_id);
    const destination = hubsByID.get(route.destination_hub_id);
    if (origin === undefined || destination === undefined) {
      return [];
    }
    const from = origin.position.clone().setY(0.08);
    const to = destination.position.clone().setY(0.08);
    const distance = from.distanceTo(to);
    const midpoint = from
      .clone()
      .add(to)
      .multiplyScalar(0.5)
      .setY(0.85 + Math.min(distance * 0.11, 2.25));
    return [{
      route,
      curve: new QuadraticBezierCurve3(from, midpoint, to),
      anomaly: anomalyByRoute.get(route.route_id),
    }];
  });
  const markers = sceneRoutes.flatMap((route, routeIndex) =>
    Array.from({ length: route.route.waybills }, (_, markerIndex) => ({
      route,
      phase:
        ((routeIndex * 0.61803398875 +
          markerIndex / Math.max(route.route.waybills, 1)) %
          1 +
          1) %
        1,
      speed: 0.025 + ((routeIndex * 7 + markerIndex * 3) % 11) * 0.002,
    })),
  );

  return {
    hubs: sceneHubs,
    hubsByID,
    routes: sceneRoutes,
    markers,
    riskHubs: sceneHubs.filter((item) => item.hub.anomalies > 0),
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
