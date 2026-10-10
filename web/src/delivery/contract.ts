import { z } from "zod";

const identifierSchema = z.string().min(1);
const timestampSchema = z.iso.datetime({ offset: true });
const digestSchema = z.string().regex(/^[a-f0-9]{64}$/);
const nonnegativeIntegerSchema = z.number().int().nonnegative();
const nonnegativeSafeIntegerSchema = z.number().int().nonnegative().safe();
const signedSafeIntegerSchema = z.number().int().safe();

export const planRevisionIdSchema = z
  .string()
  .regex(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/)
  .brand<"PlanRevisionID">();
export type PlanRevisionID = z.infer<typeof planRevisionIdSchema>;

const pointSchema = z
  .object({
    x: signedSafeIntegerSchema,
    y: signedSafeIntegerSchema,
    z: signedSafeIntegerSchema,
  })
  .strict();

const boxSchema = z
  .object({
    length: nonnegativeSafeIntegerSchema,
    width: nonnegativeSafeIntegerSchema,
    height: nonnegativeSafeIntegerSchema,
  })
  .strict();

const cuboidSchema = z
  .object({
    origin: pointSchema,
    size: boxSchema,
  })
  .strict();

const timeRangeSchema = z
  .object({
    start: timestampSchema,
    end: timestampSchema,
  })
  .strict();

const locationSchema = z
  .object({
    id: identifierSchema,
    name: z.string().min(1),
    kind: z.enum(["customer", "depot", "charger", "waypoint"]),
    latitude_microdegrees: signedSafeIntegerSchema,
    longitude_microdegrees: signedSafeIntegerSchema,
  })
  .strict();

const taskSchema = z
  .object({
    id: identifierSchema,
    kind: z.enum(["pickup", "delivery", "service", "depot_load", "depot_unload"]),
    location_id: identifierSchema,
    request_id: identifierSchema,
    hard_windows: z.array(timeRangeSchema),
    service_seconds: nonnegativeSafeIntegerSchema,
    unit_ids: z.array(identifierSchema),
  })
  .strict();

const cargoSchema = z
  .object({
    id: identifierSchema,
    unit_id: identifierSchema,
    size_mm: boxSchema,
    weight_g: nonnegativeSafeIntegerSchema,
    fragile_top_only: z.boolean(),
    temperature_zone: z.string().min(1),
    cargo_class: z.string().min(1),
  })
  .strict();

const compartmentSchema = z
  .object({
    id: identifierSchema,
    bounds: cuboidSchema,
    max_payload_g: nonnegativeSafeIntegerSchema,
    temperature_zones: z.array(z.string().min(1)),
  })
  .strict();

const doorSchema = z
  .object({
    id: identifierSchema,
    compartment_id: identifierSchema,
    opening: cuboidSchema,
    extraction_axis: z.enum(["x", "y", "z"]),
    direction: z.union([z.literal(-1), z.literal(1)]),
  })
  .strict();

const axleSchema = z
  .object({
    id: identifierSchema,
    position_x_mm: signedSafeIntegerSchema,
    max_load_g: nonnegativeSafeIntegerSchema,
  })
  .strict();

const vehicleSchema = z
  .object({
    id: identifierSchema,
    home_depot_id: identifierSchema,
    compartments: z.array(compartmentSchema).min(1),
    doors: z.array(doorSchema),
    axles: z.array(axleSchema),
    cg_envelope: z
      .object({
        min: pointSchema,
        max: pointSchema,
      })
      .strict(),
    max_gross_weight_g: nonnegativeSafeIntegerSchema,
    tare_weight_g: nonnegativeSafeIntegerSchema,
    energy: z
      .object({
        kind: z.enum(["combustion", "electric"]),
        battery_capacity_wh: nonnegativeSafeIntegerSchema,
        reserve_soc_wh: nonnegativeSafeIntegerSchema,
      })
      .strict(),
  })
  .strict();

const driverSchema = z
  .object({
    id: identifierSchema,
    shift: timeRangeSchema,
    max_continuous_drive_seconds: nonnegativeSafeIntegerSchema,
    required_break_seconds: nonnegativeSafeIntegerSchema,
    max_duty_seconds: nonnegativeSafeIntegerSchema,
  })
  .strict();

const sourceRefSchema = z
  .object({
    system: z.string().min(1),
    resource_type: z.string().min(1),
    resource_id: z.string().min(1),
    version: z.string().min(1),
    observed_at: timestampSchema,
  })
  .strict();

const stopSchema = z
  .object({
    location_id: identifierSchema,
    task_ids: z.array(identifierSchema),
    arrival_at: timestampSchema,
    service_at: timestampSchema,
    departure_at: timestampSchema,
  })
  .strict();

const dutySegmentSchema = z
  .object({
    kind: z.enum(["drive", "service", "wait", "break", "charge"]),
    driver_id: identifierSchema,
    from_location_id: identifierSchema,
    to_location_id: identifierSchema,
    start_at: timestampSchema,
    end_at: timestampSchema,
    task_ids: z.array(identifierSchema),
    charger_id: z.string(),
    charged_wh: nonnegativeSafeIntegerSchema,
  })
  .strict();

const energyLegSchema = z
  .object({
    from_stop_index: nonnegativeIntegerSchema,
    to_stop_index: nonnegativeIntegerSchema,
    start_soc_wh: nonnegativeSafeIntegerSchema,
    consumed_wh: nonnegativeSafeIntegerSchema,
    charged_wh: nonnegativeSafeIntegerSchema,
    end_soc_wh: nonnegativeSafeIntegerSchema,
    charger_id: z.string(),
  })
  .strict();

const placementSchema = z
  .object({
    cargo_id: identifierSchema,
    compartment_id: identifierSchema,
    position_mm: pointSchema,
    size_mm: boxSchema,
    orientation: z.enum(["lwh", "lhw", "wlh", "whl", "hlw", "hwl"]),
    load_at_task_id: identifierSchema,
    unload_at_task_id: identifierSchema,
    door_id: identifierSchema,
  })
  .strict();

const loadStageSchema = z
  .object({
    after_stop_index: nonnegativeIntegerSchema,
    placements: z.array(placementSchema),
    axle_loads_g: z.array(nonnegativeSafeIntegerSchema),
    center_of_mass_mm: pointSchema,
    rehandled_cargo: z.array(identifierSchema),
  })
  .strict();

const tripSchema = z
  .object({
    id: identifierSchema,
    start_depot_id: identifierSchema,
    end_depot_id: identifierSchema,
    start_at: timestampSchema,
    end_at: timestampSchema,
    stops: z.array(stopSchema).min(1),
    schedule: z.array(dutySegmentSchema),
    energy: z.array(energyLegSchema),
    load_stages: z.array(loadStageSchema).min(1),
  })
  .strict();

const vehicleDutySchema = z
  .object({
    vehicle_id: identifierSchema,
    driver_ids: z.array(identifierSchema).min(1),
    trips: z.array(tripSchema).min(1),
  })
  .strict();

export const planMetricsSchema = z
  .object({
    assigned_units: nonnegativeIntegerSchema,
    unassigned_units: nonnegativeIntegerSchema,
    vehicles_used: nonnegativeIntegerSchema,
    trips: nonnegativeIntegerSchema,
    stops: nonnegativeIntegerSchema,
    total_distance_meters: nonnegativeSafeIntegerSchema,
    total_drive_seconds: nonnegativeSafeIntegerSchema,
    total_service_seconds: nonnegativeSafeIntegerSchema,
    total_wait_seconds: nonnegativeSafeIntegerSchema,
    total_break_seconds: nonnegativeSafeIntegerSchema,
    total_charge_seconds: nonnegativeSafeIntegerSchema,
    total_energy_wh: nonnegativeSafeIntegerSchema,
    total_cost_cents: nonnegativeSafeIntegerSchema,
    stability_cost_cents: nonnegativeSafeIntegerSchema,
    on_time_tasks: nonnegativeIntegerSchema,
    late_tasks: nonnegativeIntegerSchema,
    on_time_rate_ppm: nonnegativeSafeIntegerSchema,
    min_volume_utilization_ppm: nonnegativeSafeIntegerSchema,
    mean_volume_utilization_ppm: nonnegativeSafeIntegerSchema,
    mean_payload_utilization_ppm: nonnegativeSafeIntegerSchema,
    max_payload_utilization_ppm: nonnegativeSafeIntegerSchema,
    rehandles: nonnegativeIntegerSchema,
  })
  .strict();

const objectiveSchema = z
  .object({
    unassigned_required_units: nonnegativeIntegerSchema,
    hard_violation_count: nonnegativeIntegerSchema,
    vehicles_used: nonnegativeIntegerSchema,
    total_cost_cents: nonnegativeSafeIntegerSchema,
    total_distance_meters: nonnegativeSafeIntegerSchema,
    total_wait_seconds: nonnegativeSafeIntegerSchema,
    negative_min_volume_utilization_ppm: signedSafeIntegerSchema,
    stability_cost_cents: nonnegativeSafeIntegerSchema,
  })
  .strict();

export const planSchema = z
  .object({
    schema_version: z.literal("delivery.plan.v1"),
    plan_id: identifierSchema,
    revision_id: planRevisionIdSchema,
    problem_digest: digestSchema,
    policy_digest: digestSchema,
    commitment_digest: digestSchema,
    solver: z
      .object({
        name: z.string().min(1),
        version: z.string().min(1),
        build: z.string().min(1),
      })
      .strict(),
    config_digest: digestSchema,
    duties: z.array(vehicleDutySchema).min(1),
    unassigned: z.array(
      z
        .object({
          unit_id: identifierSchema,
          reason: z.enum([
            "capacity",
            "time_window",
            "skill",
            "energy",
            "loading",
            "commitment",
            "no_vehicle",
            "manual_exclusion",
          ]),
          detail: z.string(),
        })
        .strict(),
    ),
    objective: objectiveSchema,
    metrics: planMetricsSchema,
    plan_digest: digestSchema,
  })
  .strict();

const violationSchema = z
  .object({
    code: z.string().min(1),
    severity: z.enum(["error", "warning"]),
    object: z.object({ kind: z.string().min(1), id: z.string().min(1) }).strict(),
    related: z.array(
      z.object({ kind: z.string().min(1), id: z.string().min(1) }).strict(),
    ),
    expected: z.string(),
    actual: z.string(),
    duty_index: z.number().int(),
    trip_index: z.number().int(),
    stop_index: z.number().int(),
    segment_index: z.number().int(),
  })
  .strict();

export const validationReportSchema = z
  .object({
    schema_version: z.literal("delivery.validation.v1"),
    problem_digest: digestSchema,
    policy_digest: digestSchema,
    commitment_digest: digestSchema,
    plan_digest: digestSchema,
    validator: z
      .object({
        name: z.string().min(1),
        version: z.string().min(1),
        build: z.string().min(1),
      })
      .strict(),
    valid: z.boolean(),
    violations: z.array(violationSchema),
    metrics: planMetricsSchema,
    report_digest: digestSchema,
    created_at: timestampSchema,
  })
  .strict();

const effectSchema = z
  .object({
    effect_id: identifierSchema,
    action: z.string().min(1),
    target: z.string().min(1),
    summary: z.string().min(1),
    state: z.enum(["pending", "dispatching", "succeeded", "unknown", "failed"]),
  })
  .strict();

const executionFields = {
  execution_id: identifierSchema,
  started_at: timestampSchema,
  updated_at: timestampSchema,
  effects: z.array(effectSchema).min(1),
};

const reconciliationItemSchema = z
  .object({
    effect_id: identifierSchema,
    cause: z.enum([
      "request_timeout",
      "connection_reset",
      "invalid_response",
      "provider_unavailable",
      "result_mismatch",
    ]),
    adapter: z.string().min(1),
    last_attempt_at: timestampSchema,
    idempotency_key_digest: digestSchema,
    next_check_at: timestampSchema,
  })
  .strict();

const decisionSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("not_requested") }).strict(),
  z
    .object({
      kind: z.literal("pending"),
      approval_id: identifierSchema,
      requested_at: timestampSchema,
      expires_at: timestampSchema,
      effects: z.array(effectSchema).min(1),
    })
    .strict(),
  z
    .object({
      kind: z.literal("confirmed"),
      approval_id: identifierSchema,
      decided_at: timestampSchema,
      decided_by: z.string().min(1),
      effects: z.array(effectSchema).min(1),
    })
    .strict(),
  z
    .object({
      kind: z.literal("rejected"),
      approval_id: identifierSchema,
      decided_at: timestampSchema,
      decided_by: z.string().min(1),
      reason: z.string().min(1),
    })
    .strict(),
  z
    .object({
      kind: z.literal("expired"),
      approval_id: identifierSchema,
      expired_at: timestampSchema,
    })
    .strict(),
  z
    .object({
      kind: z.literal("stale"),
      approval_id: identifierSchema,
      detected_at: timestampSchema,
      reason: z.string().min(1),
    })
    .strict(),
]);

const executionSchema = z
  .discriminatedUnion("kind", [
    z.object({ kind: z.literal("not_started") }).strict(),
    z
      .object({
        kind: z.literal("running"),
        ...executionFields,
      })
      .strict(),
    z
      .object({
        kind: z.literal("partial"),
        ...executionFields,
        finished_at: timestampSchema,
      })
      .strict(),
    z
      .object({
        kind: z.literal("reconciliation_required"),
        ...executionFields,
        reconciliation: z.array(reconciliationItemSchema).min(1),
      })
      .strict(),
    z
      .object({
        kind: z.literal("completed"),
        ...executionFields,
        committed_at: timestampSchema,
        active_revision_id: planRevisionIdSchema,
      })
      .strict(),
    z
      .object({
        kind: z.literal("failed"),
        ...executionFields,
        failed_at: timestampSchema,
        failure_code: z.string().min(1),
        detail: z.string().min(1),
      })
      .strict(),
    z
      .object({
        kind: z.literal("manual_review"),
        ...executionFields,
        entered_at: timestampSchema,
        reason: z.string().min(1),
      })
      .strict(),
  ])
  .superRefine((execution, context) => {
    if (execution.kind === "not_started") {
      return;
    }
    const states = execution.effects.map((effect) => effect.state);
    if (
      execution.kind === "running" &&
      states.some((state) => state === "unknown" || state === "failed")
    ) {
      context.addIssue({
        code: "custom",
        message: "running execution cannot contain terminal or unknown effects",
        path: ["effects"],
      });
    }
    if (
      execution.kind === "partial" &&
      (!states.includes("succeeded") || !states.includes("failed"))
    ) {
      context.addIssue({
        code: "custom",
        message: "partial execution requires succeeded and failed effects",
        path: ["effects"],
      });
    }
    if (
      execution.kind === "completed" &&
      states.some((state) => state !== "succeeded")
    ) {
      context.addIssue({
        code: "custom",
        message: "completed execution requires every effect to succeed",
        path: ["effects"],
      });
    }
    if (
      execution.kind === "failed" &&
      !states.includes("failed")
    ) {
      context.addIssue({
        code: "custom",
        message: "failed execution requires a failed effect",
        path: ["effects"],
      });
    }
    if (execution.kind === "reconciliation_required") {
      const unknownEffectIDs = execution.effects
        .filter((effect) => effect.state === "unknown")
        .map((effect) => effect.effect_id)
        .sort();
      const reconciliationEffectIDs = execution.reconciliation
        .map((item) => item.effect_id)
        .sort();
      if (
        unknownEffectIDs.length === 0 ||
        unknownEffectIDs.join("\n") !== reconciliationEffectIDs.join("\n")
      ) {
        context.addIssue({
          code: "custom",
          message: "reconciliation items must match unknown effects",
          path: ["reconciliation"],
        });
      }
    }
  });

const assignmentSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("unassigned") }).strict(),
  z
    .object({
      kind: z.literal("assigned"),
      vehicle_id: identifierSchema,
    })
    .strict(),
]);

const comparisonChangeSchema = z.discriminatedUnion("kind", [
  z
    .object({
      kind: z.literal("vehicle_assignment"),
      unit_id: identifierSchema,
      before: assignmentSchema,
      after: assignmentSchema,
    })
    .strict(),
  z
    .object({
      kind: z.literal("driver_assignment"),
      vehicle_id: identifierSchema,
      before_driver_ids: z.array(identifierSchema).min(1),
      after_driver_ids: z.array(identifierSchema).min(1),
    })
    .strict(),
  z
    .object({
      kind: z.literal("stop_sequence"),
      vehicle_id: identifierSchema,
      trip_id: identifierSchema,
      location_id: identifierSchema,
      before_index: nonnegativeIntegerSchema,
      after_index: nonnegativeIntegerSchema,
    })
    .strict(),
  z
    .object({
      kind: z.literal("eta"),
      task_id: identifierSchema,
      before_at: timestampSchema,
      after_at: timestampSchema,
      drift_seconds: signedSafeIntegerSchema,
    })
    .strict(),
  z
    .object({
      kind: z.literal("cargo_placement"),
      cargo_id: identifierSchema,
      before_stop_index: nonnegativeIntegerSchema,
      after_stop_index: nonnegativeIntegerSchema,
      before_door_id: identifierSchema,
      after_door_id: identifierSchema,
    })
    .strict(),
  z
    .object({
      kind: z.literal("metric"),
      metric: z.enum([
        "vehicles_used",
        "total_distance_meters",
        "total_cost_cents",
        "on_time_rate_ppm",
        "mean_volume_utilization_ppm",
        "stability_cost_cents",
      ]),
      before: signedSafeIntegerSchema,
      after: signedSafeIntegerSchema,
      delta: signedSafeIntegerSchema,
    })
    .strict(),
]);

const comparisonSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("unavailable") }).strict(),
  z
    .object({
      kind: z.literal("available"),
      base_revision_id: planRevisionIdSchema,
      changed_vehicle_count: nonnegativeIntegerSchema,
      changed_driver_count: nonnegativeIntegerSchema,
      reordered_stop_count: nonnegativeIntegerSchema,
      eta_drift_seconds: signedSafeIntegerSchema,
      reloaded_cargo_count: nonnegativeIntegerSchema,
      stability_cost_cents: nonnegativeSafeIntegerSchema,
      changes: z.array(comparisonChangeSchema).min(1),
    })
    .strict(),
]);

const auditEventSchema = z
  .object({
    seq: z.number().int().positive(),
    type: z.string().min(1),
    occurred_at: timestampSchema,
    actor: z.string().min(1),
    summary: z.string().min(1),
    artifact_digest: digestSchema,
  })
  .strict();

export const deliveryStreamEventSchema = z
  .object({
    schema_version: z.literal("delivery.workspace-event.v1"),
    event_id: identifierSchema,
    seq: z.number().int().positive(),
    revision_id: planRevisionIdSchema,
    workspace_version: z.number().int().positive(),
    occurred_at: timestampSchema,
    kind: z.enum([
      "run_state_changed",
      "approval_changed",
      "execution_changed",
      "revision_stale",
      "audit_appended",
    ]),
  })
  .strict();

export const deliveryWorkspaceSchema = z
  .object({
    schema_version: z.literal("delivery.workspace.v1"),
    tenant_id: identifierSchema,
    optimization_run_id: identifierSchema,
    run_state: z.enum([
      "queued",
      "solving",
      "validating",
      "candidate",
      "awaiting_approval",
      "executing",
      "completed",
      "failed",
      "manual_review",
    ]),
    attempt: z.number().int().positive(),
    created_at: timestampSchema,
    updated_at: timestampSchema,
    last_event_seq: nonnegativeIntegerSchema,
    problem: z
      .object({
        problem_id: identifierSchema,
        version: z.number().int().positive(),
        horizon: timeRangeSchema,
        locations: z.array(locationSchema).min(1),
        tasks: z.array(taskSchema),
        cargo: z.array(cargoSchema),
        vehicles: z.array(vehicleSchema).min(1),
        drivers: z.array(driverSchema).min(1),
        source_refs: z.array(sourceRefSchema).min(1),
        problem_digest: digestSchema,
      })
      .strict(),
    plan: planSchema,
    validation: validationReportSchema,
    decision: decisionSchema,
    execution: executionSchema,
    comparison: comparisonSchema,
    audit: z.array(auditEventSchema),
  })
  .strict()
  .superRefine((workspace, context) => {
    const digestsMatch =
      workspace.problem.problem_digest === workspace.plan.problem_digest &&
      workspace.validation.problem_digest === workspace.plan.problem_digest &&
      workspace.validation.policy_digest === workspace.plan.policy_digest &&
      workspace.validation.commitment_digest === workspace.plan.commitment_digest;
    if (!digestsMatch) {
      context.addIssue({
        code: "custom",
        message: "validation and problem digests do not match the plan",
        path: ["validation"],
      });
    }
    if (workspace.validation.plan_digest !== workspace.plan.plan_digest) {
      context.addIssue({
        code: "custom",
        message: "validation report does not belong to this plan",
        path: ["validation", "plan_digest"],
      });
    }
    if (workspace.validation.valid && workspace.validation.violations.length > 0) {
      context.addIssue({
        code: "custom",
        message: "valid report cannot contain violations",
        path: ["validation", "violations"],
      });
    }
    if (
      workspace.execution.kind !== "not_started" &&
      workspace.decision.kind !== "confirmed"
    ) {
      context.addIssue({
        code: "custom",
        message: "execution requires a confirmed approval",
        path: ["execution", "kind"],
      });
    }
    if (
      workspace.execution.kind !== "not_started" &&
      workspace.decision.kind === "confirmed"
    ) {
      const approvedEffectIDs = workspace.decision.effects
        .map((effect) => effect.effect_id)
        .sort();
      const executionEffectIDs = workspace.execution.effects
        .map((effect) => effect.effect_id)
        .sort();
      if (approvedEffectIDs.join("\n") !== executionEffectIDs.join("\n")) {
        context.addIssue({
          code: "custom",
          message: "execution effects do not match the confirmed approval",
          path: ["execution", "effects"],
        });
      }
    }
    if (
      workspace.execution.kind === "completed" &&
      workspace.execution.active_revision_id !== workspace.plan.revision_id
    ) {
      context.addIssue({
        code: "custom",
        message: "completed execution activated another plan revision",
        path: ["execution", "active_revision_id"],
      });
    }
    if (
      workspace.comparison.kind === "available" &&
      workspace.comparison.base_revision_id === workspace.plan.revision_id
    ) {
      context.addIssue({
        code: "custom",
        message: "comparison base must differ from the current revision",
        path: ["comparison", "base_revision_id"],
      });
    }
    workspace.audit.forEach((event, index) => {
      if (event.seq !== index + 1) {
        context.addIssue({
          code: "custom",
          message: "audit sequence must be contiguous",
          path: ["audit", index, "seq"],
        });
      }
    });
    const auditTail = workspace.audit.at(-1)?.seq ?? 0;
    if (workspace.last_event_seq !== auditTail) {
      context.addIssue({
        code: "custom",
        message: "workspace event cursor must match the audit tail",
        path: ["last_event_seq"],
      });
    }
    const locationIDs = new Set(
      workspace.problem.locations.map((location) => location.id),
    );
    const taskIDs = new Set(workspace.problem.tasks.map((task) => task.id));
    const cargoIDs = new Set(workspace.problem.cargo.map((cargo) => cargo.id));
    const vehicles = new Map(
      workspace.problem.vehicles.map((vehicle) => [vehicle.id, vehicle]),
    );
    workspace.plan.duties.forEach((duty, dutyIndex) => {
      const vehicle = vehicles.get(duty.vehicle_id);
      if (vehicle === undefined) {
        context.addIssue({
          code: "custom",
          message: "plan duty references an unknown vehicle",
          path: ["plan", "duties", dutyIndex, "vehicle_id"],
        });
        return;
      }
      const compartmentIDs = new Set(
        vehicle.compartments.map((compartment) => compartment.id),
      );
      const doorIDs = new Set(vehicle.doors.map((door) => door.id));
      duty.trips.forEach((trip, tripIndex) => {
        trip.stops.forEach((stop, stopIndex) => {
          if (!locationIDs.has(stop.location_id)) {
            context.addIssue({
              code: "custom",
              message: "plan stop references an unknown location",
              path: [
                "plan",
                "duties",
                dutyIndex,
                "trips",
                tripIndex,
                "stops",
                stopIndex,
                "location_id",
              ],
            });
          }
          stop.task_ids.forEach((taskID, taskIndex) => {
            if (!taskIDs.has(taskID)) {
              context.addIssue({
                code: "custom",
                message: "plan stop references an unknown task",
                path: [
                  "plan",
                  "duties",
                  dutyIndex,
                  "trips",
                  tripIndex,
                  "stops",
                  stopIndex,
                  "task_ids",
                  taskIndex,
                ],
              });
            }
          });
        });
        trip.load_stages.forEach((stage, stageIndex) => {
          if (stage.after_stop_index >= trip.stops.length) {
            context.addIssue({
              code: "custom",
              message: "load stage references an unknown stop",
              path: [
                "plan",
                "duties",
                dutyIndex,
                "trips",
                tripIndex,
                "load_stages",
                stageIndex,
                "after_stop_index",
              ],
            });
          }
          stage.placements.forEach((placement, placementIndex) => {
            const path = [
              "plan",
              "duties",
              dutyIndex,
              "trips",
              tripIndex,
              "load_stages",
              stageIndex,
              "placements",
              placementIndex,
            ];
            if (!cargoIDs.has(placement.cargo_id)) {
              context.addIssue({
                code: "custom",
                message: "placement references unknown cargo",
                path: [...path, "cargo_id"],
              });
            }
            if (!compartmentIDs.has(placement.compartment_id)) {
              context.addIssue({
                code: "custom",
                message: "placement references an unknown compartment",
                path: [...path, "compartment_id"],
              });
            }
            if (!doorIDs.has(placement.door_id)) {
              context.addIssue({
                code: "custom",
                message: "placement references an unknown door",
                path: [...path, "door_id"],
              });
            }
          });
        });
      });
    });
  });

export type DeliveryWorkspace = z.infer<typeof deliveryWorkspaceSchema>;
export type DeliveryPlan = z.infer<typeof planSchema>;
export type DeliveryTrip = DeliveryPlan["duties"][number]["trips"][number];
export type DeliveryLoadStage = DeliveryTrip["load_stages"][number];
export type DeliveryPlacement = DeliveryLoadStage["placements"][number];
export type DeliveryVehicle = DeliveryWorkspace["problem"]["vehicles"][number];
export type DeliveryLocation = DeliveryWorkspace["problem"]["locations"][number];
export type DeliveryTask = DeliveryWorkspace["problem"]["tasks"][number];
export type DeliveryStreamEvent = z.infer<typeof deliveryStreamEventSchema>;
