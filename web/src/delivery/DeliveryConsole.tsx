import {
  ArrowLeft,
  BatteryCharging,
  Box,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock3,
  FileCheck2,
  GitCompareArrows,
  LoaderCircle,
  Pause,
  Play,
  Radio,
  RotateCw,
  ShieldCheck,
  Truck,
  TriangleAlert,
  X,
} from "lucide-react";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
} from "react";
import { toast } from "sonner";
import {
  requestIssueCopy,
  toRequestIssue,
  type RequestIssue,
} from "../request-issue";
import { StateFeedback } from "../components/StateFeedback";
import { Badge, type BadgeTone } from "../components/ui/badge";
import { Button, ButtonLink } from "../components/ui/button";
import { Progress } from "../components/ui/progress";
import { Textarea } from "../components/ui/textarea";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "../components/ui/alert-dialog";
import { useReducedMotionPreference } from "../motion/useReducedMotionPreference";
import {
  confirmDeliveryApproval,
  rejectDeliveryApproval,
} from "./api";
import {
  deliveryConnectionLabel,
  deliveryDecisionUnavailableReason,
  type DeliveryConnectionState,
} from "./connection";
import type {
  DeliveryWorkspace,
  PlanRevisionID,
} from "./contract";
import { DeliveryCargoScene } from "./DeliveryCargoScene";
import {
  buildDeliverySteps,
  buildRoutePlot,
  formatClock,
  formatCurrency,
  formatDateTime,
  formatDistance,
  formatPercentFromPPM,
  locationKindLabel,
  resolveTripContext,
  runStateLabel,
  segmentKindLabel,
  taskKindLabel,
  type DeliveryStep,
} from "./model";
import { useDeliveryWorkspace } from "./useDeliveryWorkspace";
import styles from "./delivery-console.module.css";

type PendingDecision = "confirm" | "reject" | null;

export function DeliveryConsolePage({
  revisionID,
}: {
  revisionID: PlanRevisionID;
}) {
  const { resource, connection, reload } = useDeliveryWorkspace(revisionID);
  const [pendingDecision, setPendingDecision] =
    useState<PendingDecision>(null);

  const decide = async (
    approvalID: string,
    decision: Exclude<PendingDecision, null>,
    reason?: string,
  ) => {
    setPendingDecision(decision);
    try {
      if (decision === "confirm") {
        await confirmDeliveryApproval(approvalID);
      } else {
        await rejectDeliveryApproval(approvalID, reason ?? "");
      }
      toast.success(decision === "confirm" ? "计划已确认" : "计划已驳回");
      reload();
    } catch (error) {
      const issue = toRequestIssue(error);
      const copy = requestIssueCopy(issue);
      toast.error(copy.title, { description: copy.detail });
    } finally {
      setPendingDecision(null);
    }
  };

  if (resource.kind === "loading") {
    return <DeliveryLoadingPage />;
  }
  if (resource.kind === "error") {
    return (
      <DeliveryFailurePage
        issue={resource.issue}
        onRetry={reload}
      />
    );
  }
  return (
    <DeliveryConsoleReady
      key={resource.workspace.plan.revision_id}
      workspace={resource.workspace}
      connection={connection}
      pendingDecision={pendingDecision}
      onDecision={decide}
    />
  );
}

function DeliveryLoadingPage() {
  return (
    <DeliveryFrame>
      <StateFeedback
        className={styles.pageState}
        tone="empty"
        eyebrow="Delivery workspace"
        title="正在读取调度计划"
        detail="正在校验计划、装载和 Validator artifact。"
        action={<LoaderCircle className={styles.loadingIcon} aria-hidden="true" />}
      />
    </DeliveryFrame>
  );
}

function DeliveryFailurePage({
  issue,
  onRetry,
}: {
  issue: RequestIssue;
  onRetry: () => void;
}) {
  const copy = requestIssueCopy(issue);
  return (
    <DeliveryFrame>
      <StateFeedback
        className={styles.pageState}
        tone="error"
        eyebrow={issue.kind}
        title={copy.title}
        detail={copy.detail}
        action={
          <Button type="button" variant="secondary" onClick={onRetry}>
            <RotateCw aria-hidden="true" size={15} />
            重试
          </Button>
        }
      />
    </DeliveryFrame>
  );
}

function DeliveryFrame({ children }: { children: React.ReactNode }) {
  return (
    <>
      <a className={styles.skipLink} href="#delivery-main">
        跳到主要内容
      </a>
      <div className={styles.consoleShell}>
        <header className={styles.loadingTopbar}>
          <ButtonLink href="/" variant="ghost" size="icon" aria-label="返回经营总览">
            <ArrowLeft aria-hidden="true" size={18} />
          </ButtonLink>
          <span className={styles.brandMark} aria-hidden="true">
            DG
          </span>
          <div>
            <strong>Delivery Guardian</strong>
            <span>城市配送调度台</span>
          </div>
        </header>
        <main id="delivery-main">{children}</main>
      </div>
    </>
  );
}

export function DeliveryConsoleReady({
  workspace,
  connection = "online",
  pendingDecision = null,
  onDecision = async () => undefined,
}: {
  workspace: DeliveryWorkspace;
  connection?: DeliveryConnectionState;
  pendingDecision?: PendingDecision;
  onDecision?: (
    approvalID: string,
    decision: Exclude<PendingDecision, null>,
    reason?: string,
  ) => Promise<void>;
}) {
  const steps = useMemo(() => buildDeliverySteps(workspace), [workspace]);
  const [stepIndex, setStepIndex] = useState(0);
  const [selectedCargoID, setSelectedCargoID] = useState<string | null>(null);
  const [playing, setPlaying] = useState(false);
  const reducedMotion = useReducedMotionPreference();
  const selectedStep = steps[stepIndex];
  const tripContext =
    selectedStep === undefined
      ? null
      : resolveTripContext(workspace, selectedStep);

  useEffect(() => {
    const firstCargo =
      selectedStep?.loadStage?.placements[0]?.cargo_id ?? null;
    setSelectedCargoID(firstCargo);
  }, [selectedStep]);

  useEffect(() => {
    if (reducedMotion) {
      setPlaying(false);
    }
  }, [reducedMotion]);

  useEffect(() => {
    if (!playing || steps.length < 2) {
      return;
    }
    const timer = window.setInterval(() => {
      setStepIndex((current) => {
        if (current >= steps.length - 1) {
          setPlaying(false);
          return current;
        }
        return current + 1;
      });
    }, 1_400);
    return () => window.clearInterval(timer);
  }, [playing, steps.length]);

  const selectStep = (index: number) => {
    setPlaying(false);
    setStepIndex(Math.max(0, Math.min(index, steps.length - 1)));
  };

  if (selectedStep === undefined || tripContext === null) {
    return (
      <DeliveryFrame>
        <StateFeedback
          className={styles.pageState}
          tone="error"
          eyebrow="invalid_contract"
          title="计划缺少可展示的路线"
          detail="Plan 中的车辆、站点或位置引用不完整。"
        />
      </DeliveryFrame>
    );
  }

  return (
    <>
      <a className={styles.skipLink} href="#delivery-main">
        跳到主要内容
      </a>
      <div className={styles.consoleShell}>
        <DeliveryTopbar workspace={workspace} connection={connection} />
        <main id="delivery-main" className={styles.main}>
          <PlanMasthead workspace={workspace} />
          <section
            className={styles.workspace}
            aria-label="路线、装载与合规联动工作区"
          >
            <RoutePanel
              steps={steps}
              selectedIndex={stepIndex}
              onSelect={selectStep}
            />
            <CargoPanel
              workspace={workspace}
              step={selectedStep}
              vehicle={tripContext.vehicle}
              selectedCargoID={selectedCargoID}
              onSelectCargo={setSelectedCargoID}
            />
            <CompliancePanel
              workspace={workspace}
              step={selectedStep}
              vehicle={tripContext.vehicle}
              trip={tripContext.trip}
            />
          </section>
          <PlaybackDock
            steps={steps}
            selectedIndex={stepIndex}
            playing={playing}
            reducedMotion={reducedMotion}
            onSelect={selectStep}
            onPlayingChange={setPlaying}
          />
          <RevisionExecutionBand workspace={workspace} />
          <DecisionBand
            workspace={workspace}
            connection={connection}
            pendingDecision={pendingDecision}
            onDecision={onDecision}
          />
          <AuditBand workspace={workspace} />
        </main>
      </div>
    </>
  );
}

function DeliveryTopbar({
  workspace,
  connection,
}: {
  workspace: DeliveryWorkspace;
  connection: DeliveryConnectionState;
}) {
  return (
    <header className={styles.topbar}>
      <div className={styles.brand}>
        <ButtonLink href="/" variant="ghost" size="icon" aria-label="返回经营总览">
          <ArrowLeft aria-hidden="true" size={18} />
        </ButtonLink>
        <span className={styles.brandMark} aria-hidden="true">
          DG
        </span>
        <div>
          <strong>Delivery Guardian</strong>
          <span>城市配送调度台</span>
        </div>
      </div>
      <div className={styles.topbarContext}>
        <span>计划修订</span>
        <strong>{workspace.plan.revision_id}</strong>
      </div>
      <div className={styles.topbarSignals}>
        <Badge
          tone={runStateTone(workspace.run_state)}
          live={workspace.run_state === "solving" || workspace.run_state === "executing"}
        >
          {runStateLabel(workspace.run_state)}
        </Badge>
        <Badge tone={workspace.validation.valid ? "success" : "danger"}>
          {workspace.validation.valid ? "Validator 通过" : "Validator 拒绝"}
        </Badge>
        <Badge
          tone={connectionTone(connection)}
          live={connection === "online"}
        >
          <Radio aria-hidden="true" size={12} />
          {deliveryConnectionLabel(connection)}
        </Badge>
      </div>
      <div className={styles.topbarTime}>
        <Clock3 aria-hidden="true" size={14} />
        {formatDateTime(workspace.updated_at)} 更新
      </div>
    </header>
  );
}

function PlanMasthead({ workspace }: { workspace: DeliveryWorkspace }) {
  const metrics = workspace.plan.metrics;
  const sourceVersion = workspace.problem.source_refs
    .map((source) => source.version)
    .join(" · ");
  return (
    <section className={styles.masthead} aria-labelledby="delivery-plan-title">
      <div className={styles.planIdentity}>
        <span className={styles.eyebrow}>Dispatch plan</span>
        <h1 id="delivery-plan-title">杭州城市配送计划</h1>
        <div className={styles.planMeta}>
          <span>{workspace.problem.problem_id} / v{workspace.problem.version}</span>
          <span>{workspace.plan.solver.name} {workspace.plan.solver.version}</span>
          <span>事实水位 {sourceVersion}</span>
        </div>
      </div>
      <div className={styles.metricBand} aria-label="计划核心指标">
        <Metric label="车辆" value={String(metrics.vehicles_used)} unit="辆" />
        <Metric label="里程" value={formatDistance(metrics.total_distance_meters)} />
        <Metric label="成本" value={formatCurrency(metrics.total_cost_cents)} />
        <Metric
          label="准时率"
          value={formatPercentFromPPM(metrics.on_time_rate_ppm)}
        />
        <Metric
          label="平均装载率"
          value={formatPercentFromPPM(metrics.mean_volume_utilization_ppm)}
        />
        <Metric label="换装" value={String(metrics.rehandles)} unit="次" />
      </div>
    </section>
  );
}

function Metric({
  label,
  value,
  unit,
}: {
  label: string;
  value: string;
  unit?: string;
}) {
  return (
    <article className={styles.metric}>
      <span>{label}</span>
      <strong>{value}</strong>
      {unit !== undefined && <small>{unit}</small>}
    </article>
  );
}

function RoutePanel({
  steps,
  selectedIndex,
  onSelect,
}: {
  steps: readonly DeliveryStep[];
  selectedIndex: number;
  onSelect: (index: number) => void;
}) {
  const selected = steps[selectedIndex];
  const selection =
    selected === undefined
      ? { dutyIndex: 0, tripIndex: 0, stopIndex: 0 }
      : selected;
  const points = buildRoutePlot(steps, selection);
  const path = points
    .map((point) => `${point.xPercent},${point.yPercent}`)
    .join(" ");
  return (
    <section className={styles.routePanel} aria-labelledby="route-panel-title">
      <header className={styles.panelHeader}>
        <div>
          <span className={styles.panelIndex}>01</span>
          <h2 id="route-panel-title">路线与站序</h2>
        </div>
        <Badge tone="info">{steps.length} 站</Badge>
      </header>
      <div className={styles.routePlot}>
        <svg viewBox="0 0 100 100" role="img" aria-label="配送路线站序图">
          <polyline points={path} className={styles.routeLineShadow} />
          <polyline points={path} className={styles.routeLine} />
          {points.map((point, index) => (
            <g
              key={point.key}
              role="button"
              tabIndex={0}
              aria-label={`选择第 ${point.sequence} 站`}
              aria-current={point.selected ? "step" : undefined}
              className={styles.routePoint}
              data-selected={point.selected}
              transform={`translate(${point.xPercent} ${point.yPercent})`}
              onClick={() => onSelect(index)}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onSelect(index);
                }
              }}
            >
              <circle r={point.selected ? 4.8 : 3.8} />
              <text y="1.25" textAnchor="middle">{point.sequence}</text>
            </g>
          ))}
        </svg>
      </div>
      <ol className={styles.stopList}>
        {steps.map((step, index) => (
          <li key={step.key}>
            <button
              type="button"
              className={styles.stopButton}
              data-selected={index === selectedIndex}
              aria-current={index === selectedIndex ? "step" : undefined}
              onClick={() => onSelect(index)}
            >
              <span className={styles.stopSequence}>
                {String(step.sequence).padStart(2, "0")}
              </span>
              <span className={styles.stopCopy}>
                <strong>{step.location.name}</strong>
                <small>
                  {step.tasks.map((task) => taskKindLabel(task.kind)).join(" / ") ||
                    locationKindLabel(step.location.kind)}
                </small>
              </span>
              <time dateTime={step.serviceAt}>{formatClock(step.serviceAt)}</time>
            </button>
          </li>
        ))}
      </ol>
    </section>
  );
}

function CargoPanel({
  workspace,
  step,
  vehicle,
  selectedCargoID,
  onSelectCargo,
}: {
  workspace: DeliveryWorkspace;
  step: DeliveryStep;
  vehicle: DeliveryWorkspace["problem"]["vehicles"][number];
  selectedCargoID: string | null;
  onSelectCargo: (cargoID: string) => void;
}) {
  const stage = step.loadStage;
  const selectedPlacement = stage?.placements.find(
    (placement) => placement.cargo_id === selectedCargoID,
  );
  const selectedCargo = workspace.problem.cargo.find(
    (cargo) => cargo.id === selectedCargoID,
  );
  return (
    <section className={styles.cargoPanel} aria-labelledby="cargo-panel-title">
      <header className={styles.panelHeader}>
        <div>
          <span className={styles.panelIndex}>02</span>
          <h2 id="cargo-panel-title">逐站装卸</h2>
        </div>
        <div className={styles.panelHeaderFacts}>
          <Badge tone="neutral">{vehicle.id}</Badge>
          <Badge tone={stage?.rehandled_cargo.length === 0 ? "success" : "warning"}>
            换装 {stage?.rehandled_cargo.length ?? 0}
          </Badge>
        </div>
      </header>
      <div className={styles.cargoStage}>
        <DeliveryCargoScene
          vehicle={vehicle}
          stage={stage}
          selectedCargoID={selectedCargoID}
          onSelectCargo={onSelectCargo}
        />
        <div className={styles.cargoStageLabel}>
          <span>阶段 {String(step.sequence).padStart(2, "0")}</span>
          <strong>{step.location.name}</strong>
          <span>{stage?.placements.length ?? 0} 件在舱</span>
        </div>
      </div>
      <div className={styles.cargoInspector}>
        <div className={styles.cargoList} aria-label="当前在舱货物">
          {stage?.placements.map((placement) => (
            <button
              key={placement.cargo_id}
              type="button"
              data-selected={placement.cargo_id === selectedCargoID}
              onClick={() => onSelectCargo(placement.cargo_id)}
            >
              <Box aria-hidden="true" size={14} />
              {placement.cargo_id}
            </button>
          ))}
          {stage?.placements.length === 0 && <span>车厢已清空</span>}
        </div>
        <dl className={styles.cargoFacts}>
          <div>
            <dt>货物</dt>
            <dd>{selectedCargo?.id ?? "未选择"}</dd>
          </div>
          <div>
            <dt>尺寸</dt>
            <dd>
              {selectedCargo === undefined
                ? "—"
                : `${selectedCargo.size_mm.length} × ${selectedCargo.size_mm.width} × ${selectedCargo.size_mm.height} mm`}
            </dd>
          </div>
          <div>
            <dt>位置</dt>
            <dd>
              {selectedPlacement === undefined
                ? "—"
                : `${selectedPlacement.position_mm.x}, ${selectedPlacement.position_mm.y}, ${selectedPlacement.position_mm.z} mm`}
            </dd>
          </div>
          <div>
            <dt>卸货门</dt>
            <dd>{selectedPlacement?.door_id ?? "—"}</dd>
          </div>
        </dl>
      </div>
    </section>
  );
}

function CompliancePanel({
  workspace,
  step,
  vehicle,
  trip,
}: {
  workspace: DeliveryWorkspace;
  step: DeliveryStep;
  vehicle: DeliveryWorkspace["problem"]["vehicles"][number];
  trip: DeliveryWorkspace["plan"]["duties"][number]["trips"][number];
}) {
  const batteryCapacity = vehicle.energy.battery_capacity_wh;
  const currentSOC =
    step.socWh ??
    trip.energy[0]?.start_soc_wh ??
    batteryCapacity;
  const socPercent =
    batteryCapacity === 0 ? null : (currentSOC / batteryCapacity) * 100;
  const totalScheduleSeconds = trip.schedule.reduce(
    (total, segment) =>
      total +
      Math.max(
        1,
        (new Date(segment.end_at).getTime() -
          new Date(segment.start_at).getTime()) /
          1_000,
      ),
    0,
  );
  return (
    <aside className={styles.compliancePanel} aria-labelledby="compliance-title">
      <header className={styles.panelHeader}>
        <div>
          <span className={styles.panelIndex}>03</span>
          <h2 id="compliance-title">司机与能源</h2>
        </div>
        <Badge tone={workspace.validation.valid ? "success" : "danger"}>
          {workspace.validation.violations.length} 项违规
        </Badge>
      </header>
      <section className={styles.timelineBlock} aria-labelledby="driver-timeline-title">
        <div className={styles.subsectionHeading}>
          <Truck aria-hidden="true" size={15} />
          <h3 id="driver-timeline-title">{trip.id}</h3>
        </div>
        <div className={styles.driverMeta}>
          <span>{workspace.plan.duties[step.dutyIndex]?.driver_ids.join(" / ")}</span>
          <span>{formatClock(trip.start_at)} - {formatClock(trip.end_at)}</span>
        </div>
        <div className={styles.timelineRail} aria-label="司机活动时间轴">
          {trip.schedule.map((segment, index) => {
            const duration = Math.max(
              1,
              (new Date(segment.end_at).getTime() -
                new Date(segment.start_at).getTime()) /
                1_000,
            );
            const style: CSSProperties = {
              flexGrow: duration / Math.max(totalScheduleSeconds, 1),
            };
            return (
              <span
                key={`${segment.kind}-${segment.start_at}-${index}`}
                className={styles.timelineSegment}
                data-kind={segment.kind}
                style={style}
                title={`${segmentKindLabel(segment.kind)} ${formatClock(segment.start_at)}-${formatClock(segment.end_at)}`}
              />
            );
          })}
        </div>
        <ul className={styles.timelineLegend}>
          {trip.schedule.map((segment, index) => (
            <li key={`${segment.start_at}-${index}`}>
              <span data-kind={segment.kind} aria-hidden="true" />
              <strong>{segmentKindLabel(segment.kind)}</strong>
              <time dateTime={segment.start_at}>{formatClock(segment.start_at)}</time>
            </li>
          ))}
        </ul>
      </section>
      <section className={styles.energyBlock} aria-labelledby="energy-title">
        <div className={styles.subsectionHeading}>
          <BatteryCharging aria-hidden="true" size={15} />
          <h3 id="energy-title">车辆 SOC</h3>
        </div>
        <Progress
          value={socPercent}
          max={100}
          label={`${Math.round(currentSOC / 1_000)} / ${Math.round(batteryCapacity / 1_000)} kWh`}
          ariaLabel="当前车辆电量"
          showValue
          formatValue={(value) => `${value.toFixed(1)}%`}
          tone={socPercent !== null && socPercent < 25 ? "low" : "info"}
        />
        <div className={styles.energyLegs}>
          {trip.energy.map((leg) => (
            <div key={`${leg.from_stop_index}-${leg.to_stop_index}`}>
              <span>{leg.from_stop_index + 1} → {leg.to_stop_index + 1}</span>
              <strong>{Math.round(leg.end_soc_wh / 1_000)} kWh</strong>
            </div>
          ))}
        </div>
      </section>
      <section className={styles.validationBlock} aria-labelledby="validation-title">
        <div className={styles.subsectionHeading}>
          <ShieldCheck aria-hidden="true" size={15} />
          <h3 id="validation-title">独立校验</h3>
        </div>
        <dl className={styles.validationFacts}>
          <div>
            <dt>Validator</dt>
            <dd>{workspace.validation.validator.name} {workspace.validation.validator.version}</dd>
          </div>
          <div>
            <dt>报告</dt>
            <dd>{workspace.validation.report_digest.slice(0, 12)}</dd>
          </div>
          <div>
            <dt>硬约束</dt>
            <dd>{workspace.plan.objective.hard_violation_count}</dd>
          </div>
          <div>
            <dt>未分配</dt>
            <dd>{workspace.plan.metrics.unassigned_units}</dd>
          </div>
        </dl>
      </section>
    </aside>
  );
}

function PlaybackDock({
  steps,
  selectedIndex,
  playing,
  reducedMotion,
  onSelect,
  onPlayingChange,
}: {
  steps: readonly DeliveryStep[];
  selectedIndex: number;
  playing: boolean;
  reducedMotion: boolean;
  onSelect: (index: number) => void;
  onPlayingChange: (playing: boolean) => void;
}) {
  const step = steps[selectedIndex];
  const handleKeys = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      onSelect(selectedIndex - 1);
    }
    if (event.key === "ArrowRight") {
      event.preventDefault();
      onSelect(selectedIndex + 1);
    }
  };
  return (
    <section
      className={styles.playbackDock}
      aria-label="路线与装载联合回放"
      tabIndex={0}
      onKeyDown={handleKeys}
    >
      <div className={styles.playbackControls}>
        <Button
          type="button"
          variant="icon"
          size="icon"
          aria-label="上一个站点"
          title="上一个站点"
          disabled={selectedIndex === 0}
          onClick={() => onSelect(selectedIndex - 1)}
        >
          <ChevronLeft aria-hidden="true" size={18} />
        </Button>
        <Button
          type="button"
          variant="icon"
          size="icon"
          aria-label={playing ? "暂停回放" : "开始回放"}
          title={reducedMotion ? "系统已减少动态效果" : playing ? "暂停回放" : "开始回放"}
          disabled={reducedMotion || steps.length < 2}
          onClick={() => onPlayingChange(!playing)}
        >
          {playing ? (
            <Pause aria-hidden="true" size={17} />
          ) : (
            <Play aria-hidden="true" size={17} />
          )}
        </Button>
        <Button
          type="button"
          variant="icon"
          size="icon"
          aria-label="下一个站点"
          title="下一个站点"
          disabled={selectedIndex >= steps.length - 1}
          onClick={() => onSelect(selectedIndex + 1)}
        >
          <ChevronRight aria-hidden="true" size={18} />
        </Button>
      </div>
      <div className={styles.playbackStatus}>
        <span>STEP {String(selectedIndex + 1).padStart(2, "0")} / {String(steps.length).padStart(2, "0")}</span>
        <strong>{step?.location.name}</strong>
        <time dateTime={step?.serviceAt}>{step === undefined ? "—" : formatClock(step.serviceAt)}</time>
      </div>
      <div className={styles.playbackTrack} aria-hidden="true">
        {steps.map((item, index) => (
          <span
            key={item.key}
            data-active={index <= selectedIndex}
            data-current={index === selectedIndex}
          />
        ))}
      </div>
    </section>
  );
}

function RevisionExecutionBand({
  workspace,
}: {
  workspace: DeliveryWorkspace;
}) {
  const comparison = workspace.comparison;
  const execution = workspace.execution;
  return (
    <section
      className={styles.revisionExecutionBand}
      aria-label="修订差异与执行回写"
    >
      <article
        className={styles.revisionDiff}
        aria-labelledby="revision-diff-title"
      >
        <header className={styles.bandHeader}>
          <div>
            <span className={styles.panelIndex}>04</span>
            <GitCompareArrows aria-hidden="true" size={16} />
            <div>
              <span className={styles.eyebrow}>Revision diff</span>
              <h2 id="revision-diff-title">计划修订差异</h2>
            </div>
          </div>
          {comparison.kind === "available" && (
            <Badge tone="neutral">{comparison.base_revision_id}</Badge>
          )}
        </header>
        {comparison.kind === "unavailable" ? (
          <div className={styles.bandEmpty}>
            当前修订没有可比较的基线计划。
          </div>
        ) : (
          <>
            <dl className={styles.diffSummary}>
              <div>
                <dt>站序调整</dt>
                <dd>{comparison.reordered_stop_count}</dd>
              </div>
              <div>
                <dt>ETA 漂移</dt>
                <dd>{formatSignedDuration(comparison.eta_drift_seconds)}</dd>
              </div>
              <div>
                <dt>重新装载</dt>
                <dd>{comparison.reloaded_cargo_count}</dd>
              </div>
              <div>
                <dt>稳定性成本</dt>
                <dd>{formatCurrency(comparison.stability_cost_cents)}</dd>
              </div>
            </dl>
            <ol className={styles.diffList}>
              {comparison.changes.map((change, index) => (
                <li key={`${change.kind}-${comparisonChangeKey(change)}-${index}`}>
                  <Badge tone={comparisonChangeTone(change.kind)}>
                    {comparisonChangeLabel(change.kind)}
                  </Badge>
                  <span>
                    <strong>{comparisonChangeSubject(change)}</strong>
                    <small>{comparisonChangeDetail(change)}</small>
                  </span>
                </li>
              ))}
            </ol>
          </>
        )}
      </article>
      <article
        className={styles.executionPanel}
        aria-labelledby="execution-panel-title"
      >
        <header className={styles.bandHeader}>
          <div>
            <span className={styles.panelIndex}>05</span>
            <TriangleAlert aria-hidden="true" size={16} />
            <div>
              <span className={styles.eyebrow}>Writeback projection</span>
              <h2 id="execution-panel-title">执行与对账</h2>
            </div>
          </div>
          <Badge tone={executionTone(execution.kind)}>
            {executionTitle(execution.kind)}
          </Badge>
        </header>
        {execution.kind === "not_started" ? (
          <div className={styles.bandEmpty}>
            审批确认后，服务端将在这里投影 TMS 与 WMS 的逐项回写结果。
          </div>
        ) : (
          <div className={styles.executionBody}>
            <div className={styles.executionMeta}>
              <span>{execution.execution_id}</span>
              <time dateTime={execution.updated_at}>
                {formatDateTime(execution.updated_at)}
              </time>
            </div>
            <ul className={styles.executionEffects}>
              {execution.effects.map((effect) => (
                <li key={effect.effect_id}>
                  <span>
                    <strong>{effect.action}</strong>
                    <small>{effect.target}</small>
                  </span>
                  <Badge tone={effectStateTone(effect.state)}>
                    {effectStateLabel(effect.state)}
                  </Badge>
                </li>
              ))}
            </ul>
            {execution.kind === "reconciliation_required" && (
              <ul className={styles.reconciliationList}>
                {execution.reconciliation.map((item) => (
                  <li key={item.effect_id}>
                    <TriangleAlert aria-hidden="true" size={15} />
                    <span>
                      <strong>{reconciliationCauseLabel(item.cause)}</strong>
                      <small>
                        {item.adapter} · key {item.idempotency_key_digest.slice(0, 10)}
                        {" · "}
                        {formatDateTime(item.next_check_at)} 再查
                      </small>
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {execution.kind === "failed" && (
              <p className={styles.executionNotice}>
                {execution.failure_code} · {execution.detail}
              </p>
            )}
            {execution.kind === "manual_review" && (
              <p className={styles.executionNotice}>{execution.reason}</p>
            )}
          </div>
        )}
      </article>
    </section>
  );
}

function DecisionBand({
  workspace,
  connection,
  pendingDecision,
  onDecision,
}: {
  workspace: DeliveryWorkspace;
  connection: DeliveryConnectionState;
  pendingDecision: PendingDecision;
  onDecision: (
    approvalID: string,
    decision: Exclude<PendingDecision, null>,
    reason?: string,
  ) => Promise<void>;
}) {
  const [rejectOpen, setRejectOpen] = useState(false);
  const [reason, setReason] = useState("");
  const rejectInput = useRef<HTMLTextAreaElement>(null);
  const decision = workspace.decision;
  const effects =
    decision.kind === "pending" || decision.kind === "confirmed"
      ? decision.effects
      : [];
  const busy = pendingDecision !== null;
  const unavailableReason = deliveryDecisionUnavailableReason(connection);

  return (
    <section className={styles.decisionBand} aria-labelledby="decision-title">
      <div className={styles.decisionSummary}>
        <span className={styles.panelIndex}>06</span>
        <div>
          <span className={styles.eyebrow}>Human decision boundary</span>
          <h2 id="decision-title">{decisionTitle(decision.kind)}</h2>
          <p>{decisionDetail(workspace)}</p>
        </div>
      </div>
      <div className={styles.effectPreview}>
        {effects.map((effect) => (
          <div key={effect.effect_id}>
            <FileCheck2 aria-hidden="true" size={16} />
            <span>
              <strong>{effect.action}</strong>
              <small>{effect.summary}</small>
            </span>
            <Badge tone={effectStateTone(effect.state)}>{effectStateLabel(effect.state)}</Badge>
          </div>
        ))}
        {effects.length === 0 && (
          <span className={styles.noEffects}>当前状态没有待执行 effect</span>
        )}
      </div>
      {decision.kind === "pending" && (
        <div className={styles.decisionActions}>
          <AlertDialog open={rejectOpen} onOpenChange={setRejectOpen}>
            <AlertDialogTrigger asChild>
              <Button
                type="button"
                variant="secondary"
                disabled={busy || unavailableReason !== null}
              >
                <X aria-hidden="true" size={16} />
                驳回计划
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent
              onOpenAutoFocus={(event) => {
                event.preventDefault();
                rejectInput.current?.focus();
              }}
            >
              <form
                className={styles.rejectForm}
                onSubmit={(event) => {
                  event.preventDefault();
                  if (reason.trim() === "") {
                    rejectInput.current?.focus();
                    return;
                  }
                  void onDecision(decision.approval_id, "reject", reason.trim()).then(
                    () => {
                      setRejectOpen(false);
                      setReason("");
                    },
                  );
                }}
              >
                <AlertDialogHeader>
                  <AlertDialogTitle>驳回配送计划</AlertDialogTitle>
                  <AlertDialogDescription>
                    驳回原因将进入 Delivery 审计链，当前计划不会写入 TMS 或 WMS。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <label htmlFor="delivery-reject-reason">驳回原因</label>
                <Textarea
                  id="delivery-reject-reason"
                  ref={rejectInput}
                  rows={4}
                  value={reason}
                  required
                  disabled={busy || unavailableReason !== null}
                  onChange={(event) => setReason(event.target.value)}
                />
                <AlertDialogFooter>
                  <AlertDialogCancel asChild>
                    <Button type="button" variant="ghost" disabled={busy}>
                      取消
                    </Button>
                  </AlertDialogCancel>
                  <Button
                    type="submit"
                    variant="destructive"
                    disabled={busy || unavailableReason !== null}
                  >
                    {pendingDecision === "reject" ? (
                      <LoaderCircle className={styles.loadingIcon} aria-hidden="true" size={16} />
                    ) : (
                      <X aria-hidden="true" size={16} />
                    )}
                    确认驳回
                  </Button>
                </AlertDialogFooter>
              </form>
            </AlertDialogContent>
          </AlertDialog>
          <Button
            type="button"
            disabled={
              busy ||
              !workspace.validation.valid ||
              unavailableReason !== null
            }
            onClick={() => void onDecision(decision.approval_id, "confirm")}
          >
            {pendingDecision === "confirm" ? (
              <LoaderCircle className={styles.loadingIcon} aria-hidden="true" size={16} />
            ) : (
              <Check aria-hidden="true" size={16} />
            )}
            确认并执行
          </Button>
          {unavailableReason !== null && (
            <p className={styles.decisionLock}>{unavailableReason}</p>
          )}
        </div>
      )}
      {decision.kind !== "pending" && (
        <div className={styles.decisionTerminal}>
          <Badge tone={decisionTone(decision.kind)}>{decisionTitle(decision.kind)}</Badge>
        </div>
      )}
    </section>
  );
}

function AuditBand({ workspace }: { workspace: DeliveryWorkspace }) {
  return (
    <section className={styles.auditBand} aria-labelledby="audit-title">
      <header>
        <div>
          <span className={styles.panelIndex}>07</span>
          <h2 id="audit-title">计划审计</h2>
        </div>
        <Badge tone="neutral" tabularNums>{workspace.audit.length} EVENTS</Badge>
      </header>
      <ol>
        {workspace.audit.map((event) => (
          <li key={event.seq}>
            <span>#{String(event.seq).padStart(3, "0")}</span>
            <time dateTime={event.occurred_at}>{formatClock(event.occurred_at)}</time>
            <strong>{event.summary}</strong>
            <small>{event.actor}</small>
            <code>{event.artifact_digest.slice(0, 10)}</code>
          </li>
        ))}
      </ol>
    </section>
  );
}

function runStateTone(state: DeliveryWorkspace["run_state"]): BadgeTone {
  switch (state) {
    case "completed":
      return "success";
    case "failed":
    case "manual_review":
      return "danger";
    case "awaiting_approval":
      return "warning";
    case "solving":
    case "validating":
    case "executing":
      return "info";
    case "queued":
    case "candidate":
      return "neutral";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

type AvailableComparison = Extract<
  DeliveryWorkspace["comparison"],
  { kind: "available" }
>;
type ComparisonChange = AvailableComparison["changes"][number];
type ExecutionKind = DeliveryWorkspace["execution"]["kind"];
type ReconciliationCause = Extract<
  DeliveryWorkspace["execution"],
  { kind: "reconciliation_required" }
>["reconciliation"][number]["cause"];

function connectionTone(state: DeliveryConnectionState): BadgeTone {
  switch (state) {
    case "online":
      return "success";
    case "reconnecting":
    case "offline":
      return "warning";
    case "sealed":
      return "info";
    case "idle":
    case "connecting":
      return "neutral";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function comparisonChangeKey(change: ComparisonChange): string {
  switch (change.kind) {
    case "vehicle_assignment":
      return change.unit_id;
    case "driver_assignment":
      return change.vehicle_id;
    case "stop_sequence":
      return `${change.vehicle_id}-${change.trip_id}-${change.location_id}`;
    case "eta":
      return change.task_id;
    case "cargo_placement":
      return change.cargo_id;
    case "metric":
      return change.metric;
    default: {
      const exhaustive: never = change;
      return exhaustive;
    }
  }
}

function comparisonChangeLabel(
  kind: ComparisonChange["kind"],
): string {
  switch (kind) {
    case "vehicle_assignment":
      return "车辆";
    case "driver_assignment":
      return "司机";
    case "stop_sequence":
      return "站序";
    case "eta":
      return "ETA";
    case "cargo_placement":
      return "装载";
    case "metric":
      return "指标";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function comparisonChangeTone(
  kind: ComparisonChange["kind"],
): BadgeTone {
  switch (kind) {
    case "vehicle_assignment":
    case "driver_assignment":
      return "info";
    case "cargo_placement":
      return "warning";
    case "stop_sequence":
    case "eta":
    case "metric":
      return "neutral";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function comparisonChangeSubject(change: ComparisonChange): string {
  switch (change.kind) {
    case "vehicle_assignment":
      return change.unit_id;
    case "driver_assignment":
      return change.vehicle_id;
    case "stop_sequence":
      return `${change.vehicle_id} / ${change.trip_id}`;
    case "eta":
      return change.task_id;
    case "cargo_placement":
      return change.cargo_id;
    case "metric":
      return metricLabel(change.metric);
    default: {
      const exhaustive: never = change;
      return exhaustive;
    }
  }
}

function comparisonChangeDetail(change: ComparisonChange): string {
  switch (change.kind) {
    case "vehicle_assignment":
      return `${assignmentLabel(change.before)} → ${assignmentLabel(change.after)}`;
    case "driver_assignment":
      return `${change.before_driver_ids.join(" / ")} → ${change.after_driver_ids.join(" / ")}`;
    case "stop_sequence":
      return `第 ${change.before_index + 1} 站 → 第 ${change.after_index + 1} 站 · ${change.location_id}`;
    case "eta":
      return `${formatDateTime(change.before_at)} → ${formatDateTime(change.after_at)} · ${formatSignedDuration(change.drift_seconds)}`;
    case "cargo_placement":
      return `第 ${change.before_stop_index + 1} 站 → 第 ${change.after_stop_index + 1} 站 · ${change.before_door_id} → ${change.after_door_id}`;
    case "metric":
      return `${formatComparisonMetric(change.metric, change.before)} → ${formatComparisonMetric(change.metric, change.after)} · ${formatSignedMetric(change.metric, change.delta)}`;
    default: {
      const exhaustive: never = change;
      return exhaustive;
    }
  }
}

function assignmentLabel(
  assignment: Extract<
    ComparisonChange,
    { kind: "vehicle_assignment" }
  >["before"],
): string {
  switch (assignment.kind) {
    case "assigned":
      return assignment.vehicle_id;
    case "unassigned":
      return "未分配";
    default: {
      const exhaustive: never = assignment;
      return exhaustive;
    }
  }
}

function metricLabel(
  metric: Extract<ComparisonChange, { kind: "metric" }>["metric"],
): string {
  switch (metric) {
    case "vehicles_used":
      return "使用车辆";
    case "total_distance_meters":
      return "总里程";
    case "total_cost_cents":
      return "总成本";
    case "on_time_rate_ppm":
      return "准时率";
    case "mean_volume_utilization_ppm":
      return "平均装载率";
    case "stability_cost_cents":
      return "稳定性成本";
    default: {
      const exhaustive: never = metric;
      return exhaustive;
    }
  }
}

function formatComparisonMetric(
  metric: Extract<ComparisonChange, { kind: "metric" }>["metric"],
  value: number,
): string {
  switch (metric) {
    case "vehicles_used":
      return `${value} 辆`;
    case "total_distance_meters":
      return formatDistance(value);
    case "total_cost_cents":
    case "stability_cost_cents":
      return formatCurrency(value);
    case "on_time_rate_ppm":
    case "mean_volume_utilization_ppm":
      return formatPercentFromPPM(value);
    default: {
      const exhaustive: never = metric;
      return exhaustive;
    }
  }
}

function formatSignedMetric(
  metric: Extract<ComparisonChange, { kind: "metric" }>["metric"],
  value: number,
): string {
  const prefix = value > 0 ? "+" : "";
  switch (metric) {
    case "vehicles_used":
      return `${prefix}${value} 辆`;
    case "total_distance_meters":
      return `${prefix}${(value / 1_000).toFixed(1)} km`;
    case "total_cost_cents":
    case "stability_cost_cents":
      return `${prefix}¥${Math.round(value / 100).toLocaleString("zh-CN")}`;
    case "on_time_rate_ppm":
    case "mean_volume_utilization_ppm":
      return `${prefix}${(value / 10_000).toFixed(1)}%`;
    default: {
      const exhaustive: never = metric;
      return exhaustive;
    }
  }
}

function formatSignedDuration(seconds: number): string {
  const prefix = seconds > 0 ? "+" : seconds < 0 ? "-" : "";
  const absoluteSeconds = Math.abs(seconds);
  if (absoluteSeconds < 60) {
    return `${prefix}${absoluteSeconds} 秒`;
  }
  return `${prefix}${Math.round(absoluteSeconds / 60)} 分钟`;
}

function executionTitle(kind: ExecutionKind): string {
  switch (kind) {
    case "not_started":
      return "尚未执行";
    case "running":
      return "正在回写";
    case "partial":
      return "部分写入";
    case "reconciliation_required":
      return "等待对账";
    case "completed":
      return "回写完成";
    case "failed":
      return "回写失败";
    case "manual_review":
      return "人工复核";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function executionTone(kind: ExecutionKind): BadgeTone {
  switch (kind) {
    case "completed":
      return "success";
    case "running":
      return "info";
    case "partial":
    case "reconciliation_required":
      return "warning";
    case "failed":
    case "manual_review":
      return "danger";
    case "not_started":
      return "neutral";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function reconciliationCauseLabel(cause: ReconciliationCause): string {
  switch (cause) {
    case "request_timeout":
      return "外部请求超时";
    case "connection_reset":
      return "连接被重置";
    case "invalid_response":
      return "响应无法确认";
    case "provider_unavailable":
      return "外部服务不可用";
    case "result_mismatch":
      return "查询结果不一致";
    default: {
      const exhaustive: never = cause;
      return exhaustive;
    }
  }
}

function effectStateLabel(
  state: "pending" | "dispatching" | "succeeded" | "unknown" | "failed",
): string {
  switch (state) {
    case "pending":
      return "待执行";
    case "dispatching":
      return "执行中";
    case "succeeded":
      return "已完成";
    case "unknown":
      return "待对账";
    case "failed":
      return "失败";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function effectStateTone(
  state: "pending" | "dispatching" | "succeeded" | "unknown" | "failed",
): BadgeTone {
  switch (state) {
    case "pending":
      return "neutral";
    case "dispatching":
      return "info";
    case "succeeded":
      return "success";
    case "unknown":
      return "warning";
    case "failed":
      return "danger";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function decisionTitle(
  kind: DeliveryWorkspace["decision"]["kind"],
): string {
  switch (kind) {
    case "not_requested":
      return "尚未发起审批";
    case "pending":
      return "计划等待人工确认";
    case "confirmed":
      return "计划已确认";
    case "rejected":
      return "计划已驳回";
    case "expired":
      return "审批已过期";
    case "stale":
      return "计划事实已过期";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function decisionDetail(workspace: DeliveryWorkspace): string {
  const decision = workspace.decision;
  switch (decision.kind) {
    case "not_requested":
      return "候选计划尚未创建不可变 effect 预览。";
    case "pending":
      return `审批 ${decision.approval_id} 将于 ${formatDateTime(decision.expires_at)} 过期。`;
    case "confirmed":
      return `${decision.decided_by} 于 ${formatDateTime(decision.decided_at)} 确认。`;
    case "rejected":
      return `${decision.decided_by} 驳回：${decision.reason}`;
    case "expired":
      return `审批于 ${formatDateTime(decision.expired_at)} 自动失效。`;
    case "stale":
      return decision.reason;
    default: {
      const exhaustive: never = decision;
      return exhaustive;
    }
  }
}

function decisionTone(
  kind: DeliveryWorkspace["decision"]["kind"],
): BadgeTone {
  switch (kind) {
    case "confirmed":
      return "success";
    case "pending":
      return "warning";
    case "rejected":
    case "expired":
    case "stale":
      return "danger";
    case "not_requested":
      return "neutral";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}
