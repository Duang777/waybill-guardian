import { Canvas, type ThreeEvent } from "@react-three/fiber";
import {
  BoxGeometry,
  Color,
  EdgesGeometry,
  LineBasicMaterial,
  LineSegments,
  Matrix4,
  Object3D,
  type InstancedMesh,
} from "three";
import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type {
  DeliveryLoadStage,
  DeliveryPlacement,
  DeliveryVehicle,
} from "./contract";
import styles from "./delivery-console.module.css";

type CargoSceneProps = {
  vehicle: DeliveryVehicle;
  stage: DeliveryLoadStage | null;
  selectedCargoID: string | null;
  onSelectCargo: (cargoID: string) => void;
  forceFallback?: boolean;
};

type CargoColor = {
  fill: string;
};

const millimetersPerSceneUnit = 1_000;
const fallbackCargoColor: CargoColor = {
  fill: "#4f918b",
};
const cargoPalette: readonly CargoColor[] = [
  fallbackCargoColor,
  { fill: "#cf9850" },
  { fill: "#6b819b" },
  { fill: "#bc6f63" },
  { fill: "#859566" },
];

export function DeliveryCargoScene({
  vehicle,
  stage,
  selectedCargoID,
  onSelectCargo,
  forceFallback = false,
}: CargoSceneProps) {
  const [renderer, setRenderer] = useState<"webgl" | "fallback">(() =>
    !forceFallback && webGLAvailable() ? "webgl" : "fallback",
  );

  useEffect(() => {
    setRenderer(!forceFallback && webGLAvailable() ? "webgl" : "fallback");
  }, [forceFallback]);

  if (stage === null) {
    return (
      <div className={styles.cargoEmpty} role="status">
        当前站点没有装载阶段 artifact
      </div>
    );
  }

  if (renderer === "fallback") {
    return (
      <CargoFallback
        vehicle={vehicle}
        stage={stage}
        selectedCargoID={selectedCargoID}
        onSelectCargo={onSelectCargo}
      />
    );
  }

  return (
    <div
      className={styles.cargoCanvas}
      data-cargo-renderer="webgl"
      data-cargo-count={stage.placements.length}
    >
      <Canvas
        orthographic
        frameloop="demand"
        camera={{ position: [8.5, 6.2, 8.8], zoom: 72, near: 0.1, far: 100 }}
        dpr={[1, 1.75]}
        gl={{
          antialias: true,
          alpha: false,
          powerPreference: "high-performance",
          preserveDrawingBuffer: true,
        }}
        onCreated={({ gl, scene }) => {
          gl.setClearColor(new Color("#eef1ee"), 1);
          scene.background = new Color("#eef1ee");
          gl.domElement.dataset.sceneReady = "true";
          gl.domElement.addEventListener(
            "webglcontextlost",
            () => setRenderer("fallback"),
            { once: true },
          );
        }}
      >
        <ambientLight intensity={1.55} />
        <directionalLight position={[5, 9, 7]} intensity={2.1} />
        <CargoSceneContents
          vehicle={vehicle}
          stage={stage}
          selectedCargoID={selectedCargoID}
          onSelectCargo={onSelectCargo}
        />
      </Canvas>
    </div>
  );
}

function CargoSceneContents({
  vehicle,
  stage,
  selectedCargoID,
  onSelectCargo,
}: {
  vehicle: DeliveryVehicle;
  stage: DeliveryLoadStage;
  selectedCargoID: string | null;
  onSelectCargo: (cargoID: string) => void;
}) {
  const compartment = vehicle.compartments[0];
  if (compartment === undefined) {
    return null;
  }
  const size = compartment.bounds.size;
  const center = {
    x: size.length / millimetersPerSceneUnit / 2,
    y: size.height / millimetersPerSceneUnit / 2,
    z: size.width / millimetersPerSceneUnit / 2,
  };
  const sceneSize = {
    x: size.length / millimetersPerSceneUnit,
    y: size.height / millimetersPerSceneUnit,
    z: size.width / millimetersPerSceneUnit,
  };

  return (
    <group position={[-center.x, 0, -center.z]}>
      <CompartmentFrame center={center} size={sceneSize} />
      <AxleGuides vehicle={vehicle} compartmentWidth={sceneSize.z} />
      <CargoPlacements
        placements={stage.placements}
        selectedCargoID={selectedCargoID}
        onSelectCargo={onSelectCargo}
      />
      <mesh
        position={[
          stage.center_of_mass_mm.x / millimetersPerSceneUnit,
          Math.max(stage.center_of_mass_mm.z / millimetersPerSceneUnit, 0.08),
          stage.center_of_mass_mm.y / millimetersPerSceneUnit,
        ]}
      >
        <sphereGeometry args={[0.095, 18, 12]} />
        <meshStandardMaterial color="#d84c3f" roughness={0.7} />
      </mesh>
    </group>
  );
}

function CompartmentFrame({
  center,
  size,
}: {
  center: { x: number; y: number; z: number };
  size: { x: number; y: number; z: number };
}) {
  const lines = useMemo(() => {
    const geometry = new EdgesGeometry(
      new BoxGeometry(size.x, size.y, size.z),
    );
    return new LineSegments(
      geometry,
      new LineBasicMaterial({ color: "#60706e", transparent: true, opacity: 0.72 }),
    );
  }, [size.x, size.y, size.z]);

  useEffect(
    () => () => {
      lines.geometry.dispose();
      if (Array.isArray(lines.material)) {
        lines.material.forEach((material) => material.dispose());
      } else {
        lines.material.dispose();
      }
    },
    [lines],
  );

  return (
    <>
      <primitive object={lines} position={[center.x, center.y, center.z]} />
      <mesh
        position={[center.x, -0.025, center.z]}
        receiveShadow
      >
        <boxGeometry args={[size.x, 0.05, size.z]} />
        <meshStandardMaterial color="#d5dcda" roughness={0.92} />
      </mesh>
    </>
  );
}

function AxleGuides({
  vehicle,
  compartmentWidth,
}: {
  vehicle: DeliveryVehicle;
  compartmentWidth: number;
}) {
  return vehicle.axles.map((axle) => (
    <mesh
      key={axle.id}
      position={[
        axle.position_x_mm / millimetersPerSceneUnit,
        0.015,
        compartmentWidth / 2,
      ]}
    >
      <boxGeometry args={[0.025, 0.035, compartmentWidth + 0.24]} />
      <meshStandardMaterial color="#273534" roughness={0.82} />
    </mesh>
  ));
}

function CargoPlacements({
  placements,
  selectedCargoID,
  onSelectCargo,
}: {
  placements: readonly DeliveryPlacement[];
  selectedCargoID: string | null;
  onSelectCargo: (cargoID: string) => void;
}) {
  if (placements.length > 100) {
    return (
      <InstancedCargo
        placements={placements}
        selectedCargoID={selectedCargoID}
        onSelectCargo={onSelectCargo}
      />
    );
  }
  return placements.map((placement, index) => {
    const size = placementSceneSize(placement);
    const position = placementScenePosition(placement);
    const color = cargoColor(placement.cargo_id);
    const selected = placement.cargo_id === selectedCargoID;
    return (
      <mesh
        key={placement.cargo_id}
        position={position}
        onClick={(event) => {
          event.stopPropagation();
          onSelectCargo(placement.cargo_id);
        }}
      >
        <boxGeometry args={size} />
        <meshStandardMaterial
          color={selected ? "#d84c3f" : color.fill}
          emissive={selected ? "#5b1712" : "#000000"}
          emissiveIntensity={selected ? 0.18 : 0}
          roughness={0.78}
        />
        <mesh
          position={[0, size[1] / 2 + 0.012, 0]}
          rotation={[-Math.PI / 2, 0, 0]}
        >
          <planeGeometry args={[Math.max(size[0] - 0.04, 0.02), Math.max(size[2] - 0.04, 0.02)]} />
          <meshBasicMaterial color={cargoTopColor(index)} />
        </mesh>
      </mesh>
    );
  });
}

function InstancedCargo({
  placements,
  selectedCargoID,
  onSelectCargo,
}: {
  placements: readonly DeliveryPlacement[];
  selectedCargoID: string | null;
  onSelectCargo: (cargoID: string) => void;
}) {
  const mesh = useRef<InstancedMesh>(null);
  const transform = useMemo(() => new Object3D(), []);
  const matrix = useMemo(() => new Matrix4(), []);

  useLayoutEffect(() => {
    placements.forEach((placement, index) => {
      const size = placementSceneSize(placement);
      const position = placementScenePosition(placement);
      transform.position.set(...position);
      transform.scale.set(...size);
      transform.updateMatrix();
      matrix.copy(transform.matrix);
      mesh.current?.setMatrixAt(index, matrix);
      mesh.current?.setColorAt(
        index,
        new Color(
          placement.cargo_id === selectedCargoID
            ? "#d84c3f"
            : cargoColor(placement.cargo_id).fill,
        ),
      );
    });
    if (mesh.current !== null) {
      mesh.current.instanceMatrix.needsUpdate = true;
      if (mesh.current.instanceColor !== null) {
        mesh.current.instanceColor.needsUpdate = true;
      }
    }
  }, [matrix, placements, selectedCargoID, transform]);

  const selectInstance = (event: ThreeEvent<MouseEvent>) => {
    event.stopPropagation();
    if (event.instanceId === undefined) {
      return;
    }
    const placement = placements[event.instanceId];
    if (placement !== undefined) {
      onSelectCargo(placement.cargo_id);
    }
  };

  return (
    <instancedMesh
      ref={mesh}
      args={[undefined, undefined, placements.length]}
      onClick={selectInstance}
      frustumCulled={false}
    >
      <boxGeometry args={[1, 1, 1]} />
      <meshStandardMaterial vertexColors roughness={0.8} />
    </instancedMesh>
  );
}

function CargoFallback({
  vehicle,
  stage,
  selectedCargoID,
  onSelectCargo,
}: Omit<CargoSceneProps, "forceFallback"> & { stage: DeliveryLoadStage }) {
  const compartment = vehicle.compartments[0];
  if (compartment === undefined) {
    return (
      <div className={styles.cargoEmpty} role="status">
        车辆没有可展示的舱室
      </div>
    );
  }
  const bounds = compartment.bounds.size;
  return (
    <div
      className={styles.cargoFallback}
      data-cargo-renderer="svg"
      data-cargo-count={stage.placements.length}
    >
      <svg
        viewBox={`0 0 ${bounds.length} ${bounds.width}`}
        role="img"
        aria-label={`车厢顶视图，当前装载 ${stage.placements.length} 件货物`}
        preserveAspectRatio="xMidYMid meet"
      >
        <rect
          x="4"
          y="4"
          width={Math.max(bounds.length - 8, 0)}
          height={Math.max(bounds.width - 8, 0)}
          className={styles.fallbackCompartment}
        />
        {vehicle.axles.map((axle) => (
          <line
            key={axle.id}
            x1={axle.position_x_mm}
            x2={axle.position_x_mm}
            y1="0"
            y2={bounds.width}
            className={styles.fallbackAxle}
          />
        ))}
        {stage.placements.map((placement) => {
          const selected = placement.cargo_id === selectedCargoID;
          return (
            <g
              key={placement.cargo_id}
              role="button"
              tabIndex={0}
              aria-label={`选择货物 ${placement.cargo_id}`}
              onClick={() => onSelectCargo(placement.cargo_id)}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onSelectCargo(placement.cargo_id);
                }
              }}
              className={styles.fallbackCargo}
            >
              <rect
                x={placement.position_mm.x}
                y={placement.position_mm.y}
                width={placement.size_mm.length}
                height={placement.size_mm.width}
                fill={
                  selected
                    ? "#d84c3f"
                    : cargoColor(placement.cargo_id).fill
                }
              />
              <title>{placement.cargo_id}</title>
            </g>
          );
        })}
        <circle
          cx={stage.center_of_mass_mm.x}
          cy={stage.center_of_mass_mm.y}
          r="70"
          className={styles.fallbackCenterOfMass}
        />
      </svg>
      <span className={styles.fallbackLabel}>WebGL 降级视图 · 顶视</span>
    </div>
  );
}

function placementSceneSize(
  placement: DeliveryPlacement,
): [number, number, number] {
  return [
    placement.size_mm.length / millimetersPerSceneUnit,
    placement.size_mm.height / millimetersPerSceneUnit,
    placement.size_mm.width / millimetersPerSceneUnit,
  ];
}

function placementScenePosition(
  placement: DeliveryPlacement,
): [number, number, number] {
  const size = placementSceneSize(placement);
  return [
    placement.position_mm.x / millimetersPerSceneUnit + size[0] / 2,
    placement.position_mm.z / millimetersPerSceneUnit + size[1] / 2,
    placement.position_mm.y / millimetersPerSceneUnit + size[2] / 2,
  ];
}

function cargoColor(cargoID: string): CargoColor {
  let hash = 0;
  for (const character of cargoID) {
    hash = (hash * 31 + character.charCodeAt(0)) >>> 0;
  }
  return cargoPalette[hash % cargoPalette.length] ?? fallbackCargoColor;
}

function cargoTopColor(index: number): string {
  return index % 2 === 0 ? "#e7ece8" : "#d8e1dc";
}

function webGLAvailable(): boolean {
  if (typeof document === "undefined") {
    return false;
  }
  try {
    const canvas = document.createElement("canvas");
    return (
      canvas.getContext("webgl2") !== null ||
      canvas.getContext("webgl") !== null
    );
  } catch {
    return false;
  }
}
