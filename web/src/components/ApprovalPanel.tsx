import {
  Check,
  CheckCircle2,
  Clock3,
  CornerDownRight,
  RotateCcw,
  Send,
  X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Approval, WaybillView } from "../api";
import styles from "../app.module.css";

type ApprovalPanelProps = {
  approval: Approval | null;
  view: WaybillView | null;
  busy: boolean;
  onConfirm: () => Promise<void>;
  onReject: (reason: string) => Promise<void>;
};

export function ApprovalPanel({
  approval,
  view,
  busy,
  onConfirm,
  onReject,
}: ApprovalPanelProps) {
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const approvalTitle = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    setRejecting(false);
    setReason("");
    if (approval !== null) {
      approvalTitle.current?.focus();
    }
  }, [approval?.id]);

  if (approval === null) {
    return (
      <aside className={styles.approvalPanel} aria-labelledby="approval-title">
        <PanelHeading status="idle" />
        <div className={styles.approvalIdle}>
          <div className={styles.agentSweep} aria-hidden="true">
            <span />
          </div>
          <strong id="approval-title">等待 Agent 提交方案</strong>
          <p>Agent 完成运单、轨迹、司机与天气核验后，写操作会在这里等待确认。</p>
        </div>
      </aside>
    );
  }

  const isPending = approval.status === "pending";
  const primaryCarrier = carrierName(approval, view);

  const submitReject = async () => {
    const trimmed = reason.trim();
    if (trimmed.length === 0) {
      return;
    }
    try {
      await onReject(trimmed);
    } catch {
      return;
    }
    setRejecting(false);
    setReason("");
  };

  return (
    <aside className={styles.approvalPanel} aria-labelledby="approval-title">
      <PanelHeading status={approval.status} />
      <div className={styles.approvalBody}>
        <div className={styles.approvalLead}>
          <div>
            <span className={styles.eyebrow}>方案 v{approval.plan_version}</span>
            <h2 id="approval-title" ref={approvalTitle} tabIndex={-1}>
              {isPending
                ? `改派至${primaryCarrier}`
                : approval.status === "executed"
                  ? "方案已执行"
                  : approval.status === "reconciliation_required"
                    ? "方案等待平台对账"
                  : approval.status === "partially_failed"
                    ? "方案部分执行失败"
                    : approval.status === "failed"
                      ? "方案执行失败"
                  : approval.status === "rejected"
                    ? "方案已驳回"
                    : "正在执行方案"}
            </h2>
          </div>
          <span className={`${styles.approvalStatus} ${styles[`approvalStatus${approval.status}`]}`}>
            {approvalStatusLabel(approval.status)}
          </span>
        </div>

        <p className={styles.approvalReason}>{approval.reason}</p>

        <section className={styles.effectSection} aria-labelledby="effects-title">
          <div className={styles.sectionLabel}>
            <CornerDownRight aria-hidden="true" size={14} />
            <span id="effects-title">待执行动作</span>
          </div>
          <div className={styles.effectList}>
            {approval.items.map((item) => (
              <div
                className={styles.effectRow}
                key={"effect_id" in item ? item.effect_id : item.call_id}
              >
                <div className={styles.effectIcon}>
                  {item.action === "notify.send_sms" ? (
                    <Send aria-hidden="true" size={15} />
                  ) : (
                    <RotateCcw aria-hidden="true" size={15} />
                  )}
                </div>
                <div>
                  <strong>{actionLabel(item.action)}</strong>
                  <span>{actionDetail(item.action, item.params, view)}</span>
                </div>
              </div>
            ))}
          </div>
        </section>

        <section className={styles.evidenceSection} aria-labelledby="evidence-title">
          <div className={styles.sectionLabel}>
            <span id="evidence-title">归因证据链</span>
            <span>{approval.evidence.length} 条</span>
          </div>
          <dl className={styles.evidenceList}>
            {approval.evidence.map((evidence) => (
              <div key={evidence.label}>
                <dt>{evidence.label}</dt>
                <dd>{evidence.value}</dd>
              </div>
            ))}
          </dl>
        </section>

        <div className={styles.approvalMeta}>
          <span className={styles.mono}>{approval.id}</span>
          <span>
            <Clock3 aria-hidden="true" size={13} />
            {isPending ? `有效至 ${formatTime(approval.expires_at)}` : decisionCopy(approval)}
          </span>
        </div>

        {isPending && !rejecting && (
          <div className={styles.approvalActions}>
            <button
              className={styles.secondaryButton}
              type="button"
              disabled={busy}
              onClick={() => setRejecting(true)}
            >
              <X aria-hidden="true" size={16} />
              驳回方案
            </button>
            <button
              className={styles.primaryButton}
              type="button"
              disabled={busy}
              onClick={() => void onConfirm()}
            >
              <Check aria-hidden="true" size={17} />
              {busy ? "正在执行" : "确认并执行"}
            </button>
          </div>
        )}

        {isPending && rejecting && (
          <form
            className={styles.rejectForm}
            onSubmit={(event) => {
              event.preventDefault();
              void submitReject();
            }}
          >
            <label htmlFor="reject-reason">驳回原因</label>
            <textarea
              id="reject-reason"
              value={reason}
              rows={3}
              placeholder="例如：首选承运商当前无可用车辆"
              disabled={busy}
              onChange={(event) => setReason(event.target.value)}
            />
            <div className={styles.rejectActions}>
              <button
                className={styles.textButton}
                type="button"
                disabled={busy}
                onClick={() => setRejecting(false)}
              >
                取消
              </button>
              <button
                className={styles.dangerButton}
                type="submit"
                disabled={busy || reason.trim().length === 0}
              >
                确认驳回
              </button>
            </div>
          </form>
        )}
      </div>
    </aside>
  );
}

function PanelHeading({ status }: { status: Approval["status"] | "idle" }) {
  return (
    <div className={styles.panelHeading}>
      <div>
        <span className={styles.eyebrow}>Human-in-the-loop</span>
        <strong>人工决策闸</strong>
      </div>
      {status === "executed" ? (
        <CheckCircle2 className={styles.successIcon} aria-hidden="true" size={20} />
      ) : (
        <span className={styles.guardIndicator} aria-hidden="true" />
      )}
    </div>
  );
}

function actionLabel(action: string): string {
  switch (action) {
    case "tms.reassign":
      return "回写 TMS 改派";
    case "tms.create_claim":
      return "创建理赔单";
    case "notify.send_sms":
      return "发送通知";
    default:
      return action;
  }
}

function actionDetail(
  action: string,
  params: Readonly<Record<string, unknown>>,
  view: WaybillView | null,
): string {
  if (action === "tms.reassign") {
    const carrierID = stringParam(params, "carrier_id");
    const carrier = view?.waybill.candidate_carriers.find(
      (candidate) => candidate.carrier_id === carrierID,
    );
    return carrier === undefined
      ? carrierID
      : `${carrier.name} · 预计 ${carrier.eta_hours} 小时`;
  }
  if (action === "notify.send_sms") {
    const recipient = stringParam(params, "recipient");
    if (recipient === "shipper") {
      return "发送至货主";
    }
    if (recipient === "driver") {
      return "发送至司机";
    }
    return `发送至 ${recipient}`;
  }
  return stringParam(params, "claim_type");
}

function stringParam(params: Readonly<Record<string, unknown>>, key: string): string {
  const value = params[key];
  return typeof value === "string" ? value : "--";
}

function carrierName(approval: Approval, view: WaybillView | null): string {
  const reassign = approval.items.find((item) => item.action === "tms.reassign");
  if (reassign === undefined) {
    return "候选运力";
  }
  const carrierID = stringParam(reassign.params, "carrier_id");
  return (
    view?.waybill.candidate_carriers.find(
      (carrier) => carrier.carrier_id === carrierID,
    )?.name ?? carrierID
  );
}

function approvalStatusLabel(status: Approval["status"]): string {
  switch (status) {
    case "pending":
      return "待确认";
    case "confirmed":
      return "已确认";
    case "reconciliation_required":
      return "待对账";
    case "executed":
      return "已执行";
    case "partially_failed":
      return "部分失败";
    case "failed":
      return "执行失败";
    case "rejected":
      return "已驳回";
    case "expired":
      return "已过期";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function decisionCopy(approval: Approval): string {
  if (approval.status === "rejected" && approval.reject_reason !== undefined) {
    return approval.reject_reason;
  }
  if (approval.decided_at !== undefined) {
    return `决定于 ${formatTime(approval.decided_at)}`;
  }
  return approvalStatusLabel(approval.status);
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone: "Asia/Shanghai",
  }).format(new Date(value));
}
