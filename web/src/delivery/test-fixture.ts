import {
  deliveryWorkspaceSchema,
  type DeliveryPlacement,
  type DeliveryWorkspace,
} from "./contract";

const problemDigest = "a".repeat(64);
const policyDigest = "b".repeat(64);
const commitmentDigest = "c".repeat(64);
const configDigest = "d".repeat(64);
const planDigest = "e".repeat(64);
const reportDigest = "f".repeat(64);

function metrics() {
  return {
    assigned_units: 8,
    unassigned_units: 0,
    vehicles_used: 1,
    trips: 1,
    stops: 4,
    total_distance_meters: 86_420,
    total_drive_seconds: 7_260,
    total_service_seconds: 3_180,
    total_wait_seconds: 420,
    total_break_seconds: 1_800,
    total_charge_seconds: 1_260,
    total_energy_wh: 42_800,
    total_cost_cents: 74_360,
    stability_cost_cents: 0,
    on_time_tasks: 4,
    late_tasks: 0,
    on_time_rate_ppm: 1_000_000,
    min_volume_utilization_ppm: 412_000,
    mean_volume_utilization_ppm: 678_000,
    mean_payload_utilization_ppm: 624_000,
    max_payload_utilization_ppm: 811_000,
    rehandles: 0,
  };
}

function cargo(
  index: number,
  {
    length,
    width,
    height,
    weight,
    temperatureZone,
    cargoClass,
    fragile = false,
  }: {
    length: number;
    width: number;
    height: number;
    weight: number;
    temperatureZone: string;
    cargoClass: string;
    fragile?: boolean;
  },
) {
  return {
    id: `CARGO-${String(index).padStart(3, "0")}`,
    unit_id: `UNIT-${String(index).padStart(3, "0")}`,
    size_mm: { length, width, height },
    weight_g: weight,
    fragile_top_only: fragile,
    temperature_zone: temperatureZone,
    cargo_class: cargoClass,
  };
}

function placement(
  cargoID: string,
  position: { x: number; y: number; z: number },
  size: { length: number; width: number; height: number },
  unloadAtTaskID: string,
): DeliveryPlacement {
  return {
    cargo_id: cargoID,
    compartment_id: "COMP-MAIN",
    position_mm: position,
    size_mm: size,
    orientation: "lwh",
    load_at_task_id: "TASK-LOAD",
    unload_at_task_id: unloadAtTaskID,
    door_id: "DOOR-REAR",
  };
}

const cargoItems = [
  cargo(1, {
    length: 1_200,
    width: 1_000,
    height: 1_100,
    weight: 460_000,
    temperatureZone: "ambient",
    cargoClass: "appliance",
  }),
  cargo(2, {
    length: 1_200,
    width: 1_000,
    height: 1_100,
    weight: 440_000,
    temperatureZone: "ambient",
    cargoClass: "appliance",
  }),
  cargo(3, {
    length: 1_000,
    width: 800,
    height: 900,
    weight: 280_000,
    temperatureZone: "ambient",
    cargoClass: "general",
  }),
  cargo(4, {
    length: 1_000,
    width: 800,
    height: 900,
    weight: 270_000,
    temperatureZone: "ambient",
    cargoClass: "general",
  }),
  cargo(5, {
    length: 800,
    width: 600,
    height: 700,
    weight: 120_000,
    temperatureZone: "ambient",
    cargoClass: "fragile",
    fragile: true,
  }),
  cargo(6, {
    length: 800,
    width: 600,
    height: 700,
    weight: 110_000,
    temperatureZone: "ambient",
    cargoClass: "fragile",
    fragile: true,
  }),
  cargo(7, {
    length: 1_100,
    width: 900,
    height: 800,
    weight: 310_000,
    temperatureZone: "ambient",
    cargoClass: "general",
  }),
  cargo(8, {
    length: 1_100,
    width: 900,
    height: 800,
    weight: 300_000,
    temperatureZone: "ambient",
    cargoClass: "general",
  }),
];

const allPlacements = [
  placement(
    "CARGO-001",
    { x: 200, y: 160, z: 0 },
    { length: 1_200, width: 1_000, height: 1_100 },
    "TASK-XIAOSHAN",
  ),
  placement(
    "CARGO-002",
    { x: 1_450, y: 160, z: 0 },
    { length: 1_200, width: 1_000, height: 1_100 },
    "TASK-XIAOSHAN",
  ),
  placement(
    "CARGO-003",
    { x: 2_700, y: 160, z: 0 },
    { length: 1_000, width: 800, height: 900 },
    "TASK-SHAOXING",
  ),
  placement(
    "CARGO-004",
    { x: 3_750, y: 160, z: 0 },
    { length: 1_000, width: 800, height: 900 },
    "TASK-SHAOXING",
  ),
  placement(
    "CARGO-005",
    { x: 4_800, y: 160, z: 0 },
    { length: 800, width: 600, height: 700 },
    "TASK-YUHANG",
  ),
  placement(
    "CARGO-006",
    { x: 4_800, y: 810, z: 0 },
    { length: 800, width: 600, height: 700 },
    "TASK-YUHANG",
  ),
  placement(
    "CARGO-007",
    { x: 2_700, y: 1_030, z: 0 },
    { length: 1_100, width: 900, height: 800 },
    "TASK-SHAOXING",
  ),
  placement(
    "CARGO-008",
    { x: 3_850, y: 1_030, z: 0 },
    { length: 1_100, width: 900, height: 800 },
    "TASK-YUHANG",
  ),
];

export function deliveryWorkspaceFixture(): DeliveryWorkspace {
  return deliveryWorkspaceSchema.parse({
    schema_version: "delivery.workspace.v1",
    tenant_id: "TENANT-HZ-01",
    optimization_run_id: "OPT-20261010-0742",
    run_state: "awaiting_approval",
    attempt: 3,
    created_at: "2026-10-10T07:42:00Z",
    updated_at: "2026-10-10T07:42:18Z",
    last_event_seq: 4,
    problem: {
      problem_id: "PROBLEM-HZ-AM-1010",
      version: 7,
      horizon: {
        start: "2026-10-10T08:00:00+08:00",
        end: "2026-10-10T18:00:00+08:00",
      },
      locations: [
        {
          id: "LOC-DEPOT",
          name: "杭州传化公路港",
          kind: "depot",
          latitude_microdegrees: 30_330_000,
          longitude_microdegrees: 120_300_000,
        },
        {
          id: "LOC-XIAOSHAN",
          name: "萧山产业园",
          kind: "customer",
          latitude_microdegrees: 30_176_000,
          longitude_microdegrees: 120_274_000,
        },
        {
          id: "LOC-SHAOXING",
          name: "绍兴柯桥仓",
          kind: "customer",
          latitude_microdegrees: 30_081_000,
          longitude_microdegrees: 120_494_000,
        },
        {
          id: "LOC-YUHANG",
          name: "余杭制造基地",
          kind: "customer",
          latitude_microdegrees: 30_420_000,
          longitude_microdegrees: 120_050_000,
        },
      ],
      tasks: [
        {
          id: "TASK-LOAD",
          kind: "depot_load",
          location_id: "LOC-DEPOT",
          request_id: "REQ-LOAD",
          hard_windows: [
            {
              start: "2026-10-10T08:00:00+08:00",
              end: "2026-10-10T08:30:00+08:00",
            },
          ],
          service_seconds: 1_200,
          unit_ids: cargoItems.map((item) => item.unit_id),
        },
        {
          id: "TASK-XIAOSHAN",
          kind: "delivery",
          location_id: "LOC-XIAOSHAN",
          request_id: "REQ-XIAOSHAN",
          hard_windows: [
            {
              start: "2026-10-10T09:00:00+08:00",
              end: "2026-10-10T10:20:00+08:00",
            },
          ],
          service_seconds: 720,
          unit_ids: ["UNIT-001", "UNIT-002"],
        },
        {
          id: "TASK-SHAOXING",
          kind: "delivery",
          location_id: "LOC-SHAOXING",
          request_id: "REQ-SHAOXING",
          hard_windows: [
            {
              start: "2026-10-10T10:30:00+08:00",
              end: "2026-10-10T12:00:00+08:00",
            },
          ],
          service_seconds: 780,
          unit_ids: ["UNIT-003", "UNIT-004", "UNIT-007"],
        },
        {
          id: "TASK-YUHANG",
          kind: "delivery",
          location_id: "LOC-YUHANG",
          request_id: "REQ-YUHANG",
          hard_windows: [
            {
              start: "2026-10-10T13:30:00+08:00",
              end: "2026-10-10T15:00:00+08:00",
            },
          ],
          service_seconds: 480,
          unit_ids: ["UNIT-005", "UNIT-006", "UNIT-008"],
        },
      ],
      cargo: cargoItems,
      vehicles: [
        {
          id: "浙A-D4182",
          home_depot_id: "DEPOT-HZ",
          compartments: [
            {
              id: "COMP-MAIN",
              bounds: {
                origin: { x: 0, y: 0, z: 0 },
                size: { length: 6_200, width: 2_400, height: 2_600 },
              },
              max_payload_g: 4_800_000,
              temperature_zones: ["ambient"],
            },
          ],
          doors: [
            {
              id: "DOOR-REAR",
              compartment_id: "COMP-MAIN",
              opening: {
                origin: { x: 6_200, y: 100, z: 0 },
                size: { length: 0, width: 2_200, height: 2_400 },
              },
              extraction_axis: "x",
              direction: 1,
            },
          ],
          axles: [
            { id: "AXLE-FRONT", position_x_mm: 1_450, max_load_g: 4_600_000 },
            { id: "AXLE-REAR", position_x_mm: 4_650, max_load_g: 6_800_000 },
          ],
          cg_envelope: {
            min: { x: 1_800, y: 850, z: 0 },
            max: { x: 4_600, y: 1_550, z: 1_500 },
          },
          max_gross_weight_g: 12_000_000,
          tare_weight_g: 5_900_000,
          energy: {
            kind: "electric",
            battery_capacity_wh: 168_000,
            reserve_soc_wh: 25_200,
          },
        },
      ],
      drivers: [
        {
          id: "DRIVER-042",
          shift: {
            start: "2026-10-10T07:30:00+08:00",
            end: "2026-10-10T17:30:00+08:00",
          },
          max_continuous_drive_seconds: 14_400,
          required_break_seconds: 1_800,
          max_duty_seconds: 36_000,
        },
      ],
      source_refs: [
        {
          system: "tms-prod",
          resource_type: "order-snapshot",
          resource_id: "HZ-AM-1010",
          version: "offset-728194",
          observed_at: "2026-10-10T07:40:05Z",
        },
        {
          system: "fleet-prod",
          resource_type: "fleet-snapshot",
          resource_id: "HZ-FLEET-AM",
          version: "etag-4ac9e2",
          observed_at: "2026-10-10T07:40:07Z",
        },
      ],
      problem_digest: problemDigest,
    },
    plan: {
      schema_version: "delivery.plan.v1",
      plan_id: "PLAN-HZ-1010",
      revision_id: "REV-HZ-1010-07",
      problem_digest: problemDigest,
      policy_digest: policyDigest,
      commitment_digest: commitmentDigest,
      solver: {
        name: "guardian-deterministic",
        version: "1.0.0",
        build: "124e65c",
      },
      config_digest: configDigest,
      duties: [
        {
          vehicle_id: "浙A-D4182",
          driver_ids: ["DRIVER-042"],
          trips: [
            {
              id: "TRIP-HZ-01",
              start_depot_id: "DEPOT-HZ",
              end_depot_id: "DEPOT-HZ",
              start_at: "2026-10-10T08:00:00+08:00",
              end_at: "2026-10-10T14:26:00+08:00",
              stops: [
                {
                  location_id: "LOC-DEPOT",
                  task_ids: ["TASK-LOAD"],
                  arrival_at: "2026-10-10T08:00:00+08:00",
                  service_at: "2026-10-10T08:05:00+08:00",
                  departure_at: "2026-10-10T08:25:00+08:00",
                },
                {
                  location_id: "LOC-XIAOSHAN",
                  task_ids: ["TASK-XIAOSHAN"],
                  arrival_at: "2026-10-10T09:02:00+08:00",
                  service_at: "2026-10-10T09:05:00+08:00",
                  departure_at: "2026-10-10T09:17:00+08:00",
                },
                {
                  location_id: "LOC-SHAOXING",
                  task_ids: ["TASK-SHAOXING"],
                  arrival_at: "2026-10-10T10:33:00+08:00",
                  service_at: "2026-10-10T10:35:00+08:00",
                  departure_at: "2026-10-10T10:48:00+08:00",
                },
                {
                  location_id: "LOC-YUHANG",
                  task_ids: ["TASK-YUHANG"],
                  arrival_at: "2026-10-10T13:44:00+08:00",
                  service_at: "2026-10-10T13:46:00+08:00",
                  departure_at: "2026-10-10T13:54:00+08:00",
                },
              ],
              schedule: [
                {
                  kind: "service",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-DEPOT",
                  to_location_id: "LOC-DEPOT",
                  start_at: "2026-10-10T08:00:00+08:00",
                  end_at: "2026-10-10T08:25:00+08:00",
                  task_ids: ["TASK-LOAD"],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "drive",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-DEPOT",
                  to_location_id: "LOC-XIAOSHAN",
                  start_at: "2026-10-10T08:25:00+08:00",
                  end_at: "2026-10-10T09:02:00+08:00",
                  task_ids: [],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "service",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-XIAOSHAN",
                  to_location_id: "LOC-XIAOSHAN",
                  start_at: "2026-10-10T09:02:00+08:00",
                  end_at: "2026-10-10T09:17:00+08:00",
                  task_ids: ["TASK-XIAOSHAN"],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "drive",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-XIAOSHAN",
                  to_location_id: "LOC-SHAOXING",
                  start_at: "2026-10-10T09:17:00+08:00",
                  end_at: "2026-10-10T10:33:00+08:00",
                  task_ids: [],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "service",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-SHAOXING",
                  to_location_id: "LOC-SHAOXING",
                  start_at: "2026-10-10T10:33:00+08:00",
                  end_at: "2026-10-10T10:48:00+08:00",
                  task_ids: ["TASK-SHAOXING"],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "break",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-SHAOXING",
                  to_location_id: "LOC-SHAOXING",
                  start_at: "2026-10-10T10:48:00+08:00",
                  end_at: "2026-10-10T11:18:00+08:00",
                  task_ids: [],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "drive",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-SHAOXING",
                  to_location_id: "LOC-YUHANG",
                  start_at: "2026-10-10T11:18:00+08:00",
                  end_at: "2026-10-10T13:44:00+08:00",
                  task_ids: [],
                  charger_id: "",
                  charged_wh: 0,
                },
                {
                  kind: "service",
                  driver_id: "DRIVER-042",
                  from_location_id: "LOC-YUHANG",
                  to_location_id: "LOC-YUHANG",
                  start_at: "2026-10-10T13:44:00+08:00",
                  end_at: "2026-10-10T13:54:00+08:00",
                  task_ids: ["TASK-YUHANG"],
                  charger_id: "",
                  charged_wh: 0,
                },
              ],
              energy: [
                {
                  from_stop_index: 0,
                  to_stop_index: 1,
                  start_soc_wh: 151_200,
                  consumed_wh: 8_400,
                  charged_wh: 0,
                  end_soc_wh: 142_800,
                  charger_id: "",
                },
                {
                  from_stop_index: 1,
                  to_stop_index: 2,
                  start_soc_wh: 142_800,
                  consumed_wh: 13_600,
                  charged_wh: 0,
                  end_soc_wh: 129_200,
                  charger_id: "",
                },
                {
                  from_stop_index: 2,
                  to_stop_index: 3,
                  start_soc_wh: 129_200,
                  consumed_wh: 20_800,
                  charged_wh: 0,
                  end_soc_wh: 108_400,
                  charger_id: "",
                },
              ],
              load_stages: [
                {
                  after_stop_index: 0,
                  placements: allPlacements,
                  axle_loads_g: [3_780_000, 5_310_000],
                  center_of_mass_mm: { x: 3_220, y: 1_130, z: 462 },
                  rehandled_cargo: [],
                },
                {
                  after_stop_index: 1,
                  placements: allPlacements.slice(2),
                  axle_loads_g: [3_330_000, 4_980_000],
                  center_of_mass_mm: { x: 3_580, y: 1_180, z: 418 },
                  rehandled_cargo: [],
                },
                {
                  after_stop_index: 2,
                  placements: allPlacements.slice(4),
                  axle_loads_g: [3_050_000, 4_310_000],
                  center_of_mass_mm: { x: 4_120, y: 1_150, z: 346 },
                  rehandled_cargo: [],
                },
                {
                  after_stop_index: 3,
                  placements: [],
                  axle_loads_g: [2_920_000, 3_480_000],
                  center_of_mass_mm: { x: 3_100, y: 1_200, z: 0 },
                  rehandled_cargo: [],
                },
              ],
            },
          ],
        },
      ],
      unassigned: [],
      objective: {
        unassigned_required_units: 0,
        hard_violation_count: 0,
        vehicles_used: 1,
        total_cost_cents: 74_360,
        total_distance_meters: 86_420,
        total_wait_seconds: 420,
        negative_min_volume_utilization_ppm: -412_000,
        stability_cost_cents: 0,
      },
      metrics: metrics(),
      plan_digest: planDigest,
    },
    validation: {
      schema_version: "delivery.validation.v1",
      problem_digest: problemDigest,
      policy_digest: policyDigest,
      commitment_digest: commitmentDigest,
      plan_digest: planDigest,
      validator: {
        name: "guardian-validator",
        version: "1.0.0",
        build: "124e65c",
      },
      valid: true,
      violations: [],
      metrics: metrics(),
      report_digest: reportDigest,
      created_at: "2026-10-10T07:42:17Z",
    },
    decision: {
      kind: "pending",
      approval_id: "APPROVAL-HZ-1010-07",
      requested_at: "2026-10-10T07:42:18Z",
      expires_at: "2026-10-10T08:12:18Z",
      effects: [
        {
          effect_id: "EFFECT-TMS-1010",
          action: "tms.bind_dispatch_plan",
          target: "PLAN-HZ-1010",
          summary: "绑定 1 辆车、1 名司机与 4 个配送站点",
          state: "pending",
        },
        {
          effect_id: "EFFECT-WMS-1010",
          action: "wms.publish_loading_instruction",
          target: "浙A-D4182",
          summary: "发布 8 件货物的装车与逐站卸货指令",
          state: "pending",
        },
      ],
    },
    execution: { kind: "not_started" },
    comparison: {
      kind: "available",
      base_revision_id: "REV-HZ-1010-06",
      changed_vehicle_count: 0,
      changed_driver_count: 0,
      reordered_stop_count: 1,
      eta_drift_seconds: -420,
      reloaded_cargo_count: 2,
      stability_cost_cents: 8_400,
      changes: [
        {
          kind: "stop_sequence",
          vehicle_id: "浙A-D4182",
          trip_id: "TRIP-HZ-01",
          location_id: "LOC-SHAOXING",
          before_index: 3,
          after_index: 2,
        },
        {
          kind: "eta",
          task_id: "TASK-SHAOXING",
          before_at: "2026-10-10T10:40:00+08:00",
          after_at: "2026-10-10T10:33:00+08:00",
          drift_seconds: -420,
        },
        {
          kind: "cargo_placement",
          cargo_id: "CARGO-007",
          before_stop_index: 3,
          after_stop_index: 2,
          before_door_id: "DOOR-REAR",
          after_door_id: "DOOR-REAR",
        },
        {
          kind: "metric",
          metric: "total_distance_meters",
          before: 89_760,
          after: 86_420,
          delta: -3_340,
        },
      ],
    },
    audit: [
      {
        seq: 1,
        type: "problem_frozen",
        occurred_at: "2026-10-10T07:42:01Z",
        actor: "snapshot-builder",
        summary: "冻结订单、车队、司机与路网事实",
        artifact_digest: problemDigest,
      },
      {
        seq: 2,
        type: "solver_completed",
        occurred_at: "2026-10-10T07:42:16Z",
        actor: "guardian-deterministic",
        summary: "在固定预算内生成候选计划",
        artifact_digest: planDigest,
      },
      {
        seq: 3,
        type: "validation_passed",
        occurred_at: "2026-10-10T07:42:17Z",
        actor: "guardian-validator",
        summary: "独立 Validator 确认硬约束违规为零",
        artifact_digest: reportDigest,
      },
      {
        seq: 4,
        type: "approval_requested",
        occurred_at: "2026-10-10T07:42:18Z",
        actor: "delivery-service",
        summary: "创建计划审批并冻结 effect 预览",
        artifact_digest: planDigest,
      },
    ],
  });
}

export type DeliveryFixtureState =
  | "normal"
  | "empty"
  | "failed"
  | "expired"
  | "stale"
  | "rejected"
  | "approved"
  | "partial"
  | "reconciliation";

type DeliveryEffect = Extract<
  DeliveryWorkspace["decision"],
  { kind: "pending" }
>["effects"][number];

export function deliveryWorkspaceStateFixture(
  state: DeliveryFixtureState,
): DeliveryWorkspace {
  const workspace = structuredClone(deliveryWorkspaceFixture());
  if (state === "normal") {
    return workspace;
  }

  if (state === "empty") {
    workspace.problem.cargo = [];
    workspace.problem.tasks.forEach((task) => {
      task.unit_ids = [];
    });
    workspace.plan.duties.forEach((duty) => {
      duty.trips.forEach((trip) => {
        trip.load_stages.forEach((stage) => {
          stage.placements = [];
          stage.rehandled_cargo = [];
        });
      });
    });
    workspace.plan.metrics.assigned_units = 0;
    workspace.plan.metrics.mean_volume_utilization_ppm = 0;
    workspace.plan.metrics.min_volume_utilization_ppm = 0;
    workspace.plan.metrics.mean_payload_utilization_ppm = 0;
    workspace.plan.metrics.max_payload_utilization_ppm = 0;
    workspace.validation.metrics = structuredClone(workspace.plan.metrics);
    workspace.comparison = { kind: "unavailable" };
    return deliveryWorkspaceSchema.parse(workspace);
  }

  const sourceEffects =
    workspace.decision.kind === "pending"
      ? workspace.decision.effects
      : [];
  const pendingEffects = sourceEffects.map(
    (effect): DeliveryEffect => ({ ...effect, state: "pending" }),
  );

  switch (state) {
    case "failed":
      workspace.run_state = "failed";
      workspace.decision = { kind: "not_requested" };
      appendAudit(workspace, "run_failed", "求解服务返回不可恢复错误");
      break;
    case "expired":
      workspace.run_state = "candidate";
      workspace.decision = {
        kind: "expired",
        approval_id: "APPROVAL-HZ-1010-07",
        expired_at: "2026-10-10T08:12:18Z",
      };
      appendAudit(workspace, "approval_expired", "审批超过有效期，计划未执行");
      break;
    case "stale":
      workspace.run_state = "manual_review";
      workspace.decision = {
        kind: "stale",
        approval_id: "APPROVAL-HZ-1010-07",
        detected_at: "2026-10-10T07:48:00Z",
        reason: "车队事实水位已变化，需要基于新快照重新优化。",
      };
      appendAudit(workspace, "revision_stale", "车队事实变化使当前修订失效");
      break;
    case "rejected":
      workspace.run_state = "candidate";
      workspace.decision = {
        kind: "rejected",
        approval_id: "APPROVAL-HZ-1010-07",
        decided_at: "2026-10-10T07:45:00Z",
        decided_by: "dispatcher-042",
        reason: "客户临时调整绍兴站时间窗",
      };
      appendAudit(workspace, "approval_rejected", "调度员驳回当前计划修订");
      break;
    case "approved":
      workspace.run_state = "executing";
      workspace.decision = {
        kind: "confirmed",
        approval_id: "APPROVAL-HZ-1010-07",
        decided_at: "2026-10-10T07:45:00Z",
        decided_by: "dispatcher-042",
        effects: pendingEffects,
      };
      appendAudit(workspace, "approval_confirmed", "调度员确认计划并冻结 effect 集合");
      break;
    case "partial": {
      const effects = [
        effectWithState(sourceEffects[0], "succeeded"),
        effectWithState(sourceEffects[1], "failed"),
      ].filter((effect): effect is DeliveryEffect => effect !== undefined);
      workspace.run_state = "manual_review";
      workspace.decision = {
        kind: "confirmed",
        approval_id: "APPROVAL-HZ-1010-07",
        decided_at: "2026-10-10T07:45:00Z",
        decided_by: "dispatcher-042",
        effects,
      };
      workspace.execution = {
        kind: "partial",
        execution_id: "EXECUTION-HZ-1010-07",
        started_at: "2026-10-10T07:45:01Z",
        updated_at: "2026-10-10T07:46:20Z",
        finished_at: "2026-10-10T07:46:20Z",
        effects,
      };
      appendAudit(workspace, "execution_partial", "TMS 已写入，WMS 发布失败");
      break;
    }
    case "reconciliation": {
      const effects = [
        effectWithState(sourceEffects[0], "succeeded"),
        effectWithState(sourceEffects[1], "unknown"),
      ].filter((effect): effect is DeliveryEffect => effect !== undefined);
      const unknownEffect = effects.find((effect) => effect.state === "unknown");
      if (unknownEffect === undefined) {
        throw new Error("reconciliation fixture requires an unknown effect");
      }
      workspace.run_state = "executing";
      workspace.decision = {
        kind: "confirmed",
        approval_id: "APPROVAL-HZ-1010-07",
        decided_at: "2026-10-10T07:45:00Z",
        decided_by: "dispatcher-042",
        effects,
      };
      workspace.execution = {
        kind: "reconciliation_required",
        execution_id: "EXECUTION-HZ-1010-07",
        started_at: "2026-10-10T07:45:01Z",
        updated_at: "2026-10-10T07:46:20Z",
        effects,
        reconciliation: [
          {
            effect_id: unknownEffect.effect_id,
            cause: "request_timeout",
            adapter: "wms-prod",
            last_attempt_at: "2026-10-10T07:46:18Z",
            idempotency_key_digest: "9".repeat(64),
            next_check_at: "2026-10-10T07:47:18Z",
          },
        ],
      };
      appendAudit(workspace, "reconciliation_required", "WMS 返回结果未知，进入只查不写对账");
      break;
    }
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }

  return deliveryWorkspaceSchema.parse(workspace);
}

export function deliveryLargeWorkspaceFixture(
  cargoCount = 300,
): DeliveryWorkspace {
  const workspace = structuredClone(deliveryWorkspaceFixture());
  const largeCargo = Array.from({ length: cargoCount }, (_, index) =>
    cargo(index + 1, {
      length: 250,
      width: 350,
      height: 300,
      weight: 5_000,
      temperatureZone: "ambient",
      cargoClass: "parcel",
    }),
  );
  const largePlacements = largeCargo.map((item, index) => {
    const deliveryTaskID =
      index < Math.ceil(cargoCount / 3)
        ? "TASK-XIAOSHAN"
        : index < Math.ceil((cargoCount * 2) / 3)
          ? "TASK-SHAOXING"
          : "TASK-YUHANG";
    return placement(
      item.id,
      {
        x: (index % 20) * 300,
        y: (Math.floor(index / 20) % 5) * 450,
        z: Math.floor(index / 100) * 350,
      },
      item.size_mm,
      deliveryTaskID,
    );
  });

  workspace.problem.cargo = largeCargo;
  const loadTask = workspace.problem.tasks.find(
    (task) => task.id === "TASK-LOAD",
  );
  if (loadTask === undefined) {
    throw new Error("large fixture requires the depot load task");
  }
  loadTask.unit_ids = largeCargo.map((item) => item.unit_id);
  for (const task of workspace.problem.tasks) {
    if (task.id === "TASK-XIAOSHAN") {
      task.unit_ids = largeCargo
        .slice(0, Math.ceil(cargoCount / 3))
        .map((item) => item.unit_id);
    } else if (task.id === "TASK-SHAOXING") {
      task.unit_ids = largeCargo
        .slice(
          Math.ceil(cargoCount / 3),
          Math.ceil((cargoCount * 2) / 3),
        )
        .map((item) => item.unit_id);
    } else if (task.id === "TASK-YUHANG") {
      task.unit_ids = largeCargo
        .slice(Math.ceil((cargoCount * 2) / 3))
        .map((item) => item.unit_id);
    }
  }
  const trip = workspace.plan.duties[0]?.trips[0];
  if (trip === undefined) {
    throw new Error("large fixture requires one trip");
  }
  const firstCut = Math.ceil(cargoCount / 3);
  const secondCut = Math.ceil((cargoCount * 2) / 3);
  const stages = [
    largePlacements,
    largePlacements.slice(firstCut),
    largePlacements.slice(secondCut),
    [],
  ];
  trip.load_stages.forEach((stage, index) => {
    stage.placements = stages[index] ?? [];
    stage.rehandled_cargo = [];
  });
  workspace.plan.metrics.assigned_units = cargoCount;
  workspace.validation.metrics = structuredClone(workspace.plan.metrics);
  if (workspace.decision.kind === "pending") {
    const loadingEffect = workspace.decision.effects.find(
      (effect) => effect.action === "wms.publish_loading_instruction",
    );
    if (loadingEffect !== undefined) {
      loadingEffect.summary = `发布 ${cargoCount} 件货物的装车与逐站卸货指令`;
    }
  }
  return deliveryWorkspaceSchema.parse(workspace);
}

function effectWithState(
  effect: DeliveryEffect | undefined,
  state: DeliveryEffect["state"],
): DeliveryEffect | undefined {
  return effect === undefined ? undefined : { ...effect, state };
}

function appendAudit(
  workspace: DeliveryWorkspace,
  type: string,
  summary: string,
): void {
  const seq = workspace.audit.length + 1;
  const occurredAt = "2026-10-10T07:48:00Z";
  workspace.audit.push({
    seq,
    type,
    occurred_at: occurredAt,
    actor: "delivery-service",
    summary,
    artifact_digest: workspace.plan.plan_digest,
  });
  workspace.last_event_seq = seq;
  workspace.updated_at = occurredAt;
}
