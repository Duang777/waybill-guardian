import { Clock3, CloudSun, Route, ShieldCheck, Truck } from "lucide-react";
import type { RunStatus } from "../api";
import styles from "../app.module.css";
import type { WaybillResource } from "../waybill-resource";
import {
  HaloBadge,
  HaloProgress,
  type HaloBadgeTone,
  type HaloProgressTone,
} from "./cult";

type SummaryStripProps = {
  resource: WaybillResource;
  status: RunStatus | null;
  connected: boolean;
};

const statusLabels = {
  started: "已启动",
  investigating: "正在归因",
  awaiting_approval: "等待审批",
  executing: "正在回写",
  completed: "处置完成",
  rejected: "转人工跟进",
  failed: "处置失败",
  review_required: "提案待复核",
  manual_review: "等待人工复核",
} satisfies Record<RunStatus, string>;

export function SummaryStrip({
  resource,
  status,
  connected,
}: SummaryStripProps) {
  const view = resource.kind === "ready" ? resource.view : null;
  const waybillID = resource.kind === "empty" ? null : resource.waybillID;
  const route = routeLabel(resource);
  const cargo =
    resource.kind === "ready"
      ? resource.view.waybill.cargo
      : resource.kind === "error"
        ? "加载失败"
        : resource.kind === "loading"
          ? "读取中"
          : "--";
  return (
    <section className={styles.summaryStrip} aria-label="运单状态摘要">
      <div className={styles.routeSummary}>
        <div className={styles.routeIcon}>
          <Route aria-hidden="true" size={19} />
        </div>
        <div>
          <span className={styles.eyebrow}>异常运单</span>
          <strong className={styles.routeTitle}>{route}</strong>
          <span className={styles.mono}>{waybillID ?? "尚未选择运单"}</span>
        </div>
      </div>

      <dl className={styles.shipmentFacts}>
        <div>
          <dt>
            <Truck aria-hidden="true" size={14} />
            货物
          </dt>
          <dd>{cargo}</dd>
        </div>
        <div>
          <dt>
            <Clock3 aria-hidden="true" size={14} />
            时效
          </dt>
          <dd>{view === null ? "--" : `${view.waybill.sla_hours} 小时`}</dd>
        </div>
        <div>
          <dt>
            <CloudSun aria-hidden="true" size={14} />
            天气
          </dt>
          <dd>{view?.weather[0]?.condition ?? "--"}</dd>
        </div>
        <div>
          <dt>
            <ShieldCheck aria-hidden="true" size={14} />
            Agent
          </dt>
          <dd>
            <HaloBadge tone={agentStatusTone(status)} live={connected}>
              {status === null ? "待启动" : statusLabels[status]}
            </HaloBadge>
          </dd>
        </div>
      </dl>

      <div className={styles.riskCluster}>
        <RiskBar label="ETA 延误" value={view?.risk.eta_delay ?? 0} tone="high" />
        <RiskBar label="路况" value={view?.risk.road ?? 0} tone="medium" />
        <RiskBar label="天气" value={view?.risk.weather ?? 0} tone="low" />
      </div>
    </section>
  );
}

function routeLabel(resource: WaybillResource): string {
  switch (resource.kind) {
    case "empty":
      return "暂无可处置运单";
    case "loading":
      return "正在读取路线";
    case "error":
      return "路线加载失败";
    case "ready":
      return `${resource.view.waybill.origin} → ${resource.view.waybill.destination}`;
    default: {
      const exhaustive: never = resource;
      return exhaustive;
    }
  }
}

type RiskBarProps = {
  label: string;
  value: number;
  tone: HaloProgressTone;
};

function RiskBar({ label, value, tone }: RiskBarProps) {
  return (
    <HaloProgress
      value={value}
      label={label}
      showValue
      tone={tone}
      ariaLabel={`${label}风险 ${value} 分`}
    />
  );
}

function agentStatusTone(status: RunStatus | null): HaloBadgeTone {
  switch (status) {
    case "completed":
      return "success";
    case "started":
    case "investigating":
    case "executing":
      return "info";
    case "awaiting_approval":
    case "review_required":
    case "manual_review":
      return "warning";
    case "rejected":
    case "failed":
      return "danger";
    case null:
      return "neutral";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}
