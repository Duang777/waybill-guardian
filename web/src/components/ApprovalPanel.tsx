import {
  Check,
  CheckCircle2,
  Clock3,
  CornerDownRight,
  Crosshair,
  RotateCcw,
  Send,
  TriangleAlert,
  X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Approval, Proposal, RunStatus, WaybillView } from "../api";
import styles from "../app.module.css";
import type { EvidenceSelection } from "../timeline";
import {
  HaloBadge,
  TextureButton,
  TimerDisplay,
  TimerIcon,
  TimerRoot,
  type HaloBadgeTone,
} from "./cult";

type ApprovalPanelProps = {
  approval: Approval | null;
  proposal: Proposal | null;
  runStatus: RunStatus | null;
  view: WaybillView | null;
  busy: boolean;
  embedded?: boolean;
  showEvidence?: boolean;
  onEvidenceSelect: (selection: EvidenceSelection) => void;
  onConfirm: () => Promise<void>;
  onReject: (reason: string) => Promise<void>;
};

export function ApprovalPanel({
  approval,
  proposal,
  runStatus,
  view,
  busy,
  embedded = false,
  showEvidence = true,
  onEvidenceSelect,
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

  if (runStatus === "review_required") {
    return (
      <section
        className={`${styles.approvalPanel} ${
          embedded ? styles.approvalPanelEmbedded : ""
        }`}
        aria-labelledby="approval-title"
      >
        <PanelHeading status="review_required" />
        <div className={styles.approvalIdle}>
          <TriangleAlert aria-hidden="true" size={22} />
          <strong id="approval-title">提案需要人工复核</strong>
          <p>模型输出未通过证据校验，本次运行没有生成可执行审批。</p>
        </div>
      </section>
    );
  }

  if (approval === null) {
    return (
      <section
        className={`${styles.approvalPanel} ${
          embedded ? styles.approvalPanelEmbedded : ""
        }`}
        aria-labelledby="approval-title"
      >
        <PanelHeading status="idle" />
        <div className={styles.approvalIdle}>
          <div className={styles.agentSweep} aria-hidden="true">
            <span />
          </div>
          <strong id="approval-title">等待 Agent 提交方案</strong>
          <p>Agent 完成运单、轨迹、司机与天气核验后，写操作会在这里等待确认。</p>
        </div>
      </section>
    );
  }

  const isPending = approval.status === "pending";
  const selectedCarrierID = carrierID(approval);
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
    <section
      className={`${styles.approvalPanel} ${
        isPending ? styles.approvalPanelPending : ""
      } ${embedded ? styles.approvalPanelEmbedded : ""}`}
      aria-labelledby="approval-title"
    >
      <PanelHeading status={approval.status} />
      <div className={styles.approvalBody}>
        <div className={styles.approvalLead}>
          <div>
            <span className={styles.eyebrow}>方案 v{approval.plan_version}</span>
            <h2 id="approval-title" ref={approvalTitle} tabIndex={-1}>
              {approvalHeading(approval.status, primaryCarrier)}
            </h2>
          </div>
          <div className={styles.approvalBadges}>
            {proposal !== null && (
              <HaloBadge tone="info" tabularNums>
                置信度 {formatConfidence(proposal.confidence_bps)}
              </HaloBadge>
            )}
            <HaloBadge tone={approvalStatusTone(approval.status)}>
              {approvalStatusLabel(approval.status)}
            </HaloBadge>
          </div>
        </div>

        <p className={styles.approvalReason}>{approval.reason}</p>

        {proposal !== null && (
          <section className={styles.proposalSection} aria-labelledby="proposal-title">
            <div className={styles.sectionLabel}>
              <span id="proposal-title">候选方案</span>
              <span>{proposal.alternatives.length} 个</span>
            </div>
            <div className={styles.alternativeList}>
              {proposal.alternatives.map((alternative) => (
                <div key={alternative.carrier_id}>
                  <div className={styles.alternativeHeading}>
                    <strong>{carrierDisplayName(alternative.carrier_id, view)}</strong>
                    <HaloBadge
                      tone={
                        alternative.carrier_id === selectedCarrierID
                          ? "info"
                          : "neutral"
                      }
                    >
                      {alternative.carrier_id === selectedCarrierID
                        ? "首选"
                        : "备选"}
                    </HaloBadge>
                  </div>
                  <span className={styles.alternativeReason}>
                    {alternative.reason}
                  </span>
                </div>
              ))}
            </div>
            <dl className={styles.impactList}>
              <div>
                <dt>预计时效挽回</dt>
                <dd title={proposal.expected_impact.eta_saved_min.reason}>证据不足</dd>
              </div>
              <div>
                <dt>预计成本变化</dt>
                <dd title={proposal.expected_impact.cost_delta_cny.reason}>证据不足</dd>
              </div>
            </dl>
          </section>
        )}

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

        {showEvidence && (
          <section className={styles.evidenceSection} aria-labelledby="evidence-title">
            <div className={styles.sectionLabel}>
              <span id="evidence-title">归因证据链</span>
              <span>{approval.evidence.length} 条</span>
            </div>
            <dl className={styles.evidenceList}>
              {approval.evidence.map((evidence) => (
                <div
                  key={`${evidence.label}-${
                    "source" in evidence
                      ? `${evidence.source.source_seq}-${evidence.source.field_path}`
                      : evidence.value
                  }`}
                >
                  <dt>{evidence.label}</dt>
                  <dd>
                    {"source" in evidence ? (
                      <button
                        className={styles.evidenceLink}
                        type="button"
                        aria-label={`定位证据：${evidence.label}，审计事件 ${evidence.source.source_seq}`}
                        title={`定位到审计事件 #${evidence.source.source_seq}`}
                        onClick={() =>
                          onEvidenceSelect({
                            sourceSeq: evidence.source.source_seq,
                            fieldPath: evidence.source.field_path,
                          })
                        }
                      >
                        <span>{evidence.value}</span>
                        <Crosshair aria-hidden="true" size={13} />
                      </button>
                    ) : (
                      evidence.value
                    )}
                  </dd>
                  {"source" in evidence && (
                    <span className={styles.evidencePath}>
                      {evidence.source.field_path}
                    </span>
                  )}
                </div>
              ))}
            </dl>
          </section>
        )}

        <div className={styles.approvalMeta}>
          <span className={styles.mono}>{approval.id}</span>
          {isPending ? (
            <ApprovalDeadline expiresAt={approval.expires_at} />
          ) : (
            <span>
              <Clock3 aria-hidden="true" size={13} />
              {decisionCopy(approval)}
            </span>
          )}
        </div>

        {isPending && !rejecting && (
          <div className={styles.approvalActions}>
            <TextureButton
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={() => setRejecting(true)}
            >
              <X aria-hidden="true" size={16} />
              驳回方案
            </TextureButton>
            <TextureButton
              type="button"
              variant="primary"
              disabled={busy}
              onClick={() => void onConfirm()}
            >
              <Check aria-hidden="true" size={17} />
              {busy ? "正在执行" : "确认并执行"}
            </TextureButton>
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
              <TextureButton
                type="button"
                variant="minimal"
                size="sm"
                disabled={busy}
                onClick={() => setRejecting(false)}
              >
                取消
              </TextureButton>
              <TextureButton
                type="submit"
                variant="destructive"
                size="sm"
                disabled={busy || reason.trim().length === 0}
              >
                确认驳回
              </TextureButton>
            </div>
          </form>
        )}
      </div>
    </section>
  );
}

function PanelHeading({
  status,
}: {
  status: Approval["status"] | "review_required" | "idle";
}) {
  return (
    <div
      className={`${styles.panelHeading} ${
        styles[`panelHeading${headingTone(status)}`]
      }`}
    >
      <div>
        <span className={styles.eyebrow}>人工审批</span>
        <strong>人工决策闸</strong>
      </div>
      {status === "executed" ? (
        <CheckCircle2 className={styles.successIcon} aria-hidden="true" size={20} />
      ) : status === "review_required" ? (
        <TriangleAlert className={styles.reviewIcon} aria-hidden="true" size={20} />
      ) : status === "rejected" ||
        status === "expired" ||
        status === "failed" ||
        status === "partially_failed" ? (
        <X className={styles.failureIcon} aria-hidden="true" size={20} />
      ) : (
        <span className={styles.guardIndicator} aria-hidden="true" />
      )}
    </div>
  );
}

function ApprovalDeadline({ expiresAt }: { expiresAt: string }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  const remaining = remainingTimeLabel(Date.parse(expiresAt) - now);
  return (
    <TimerRoot title={`有效至 ${formatTime(expiresAt)}`}>
      <TimerIcon />
      <TimerDisplay time={remaining} label={`审批${remaining}`} />
    </TimerRoot>
  );
}

function remainingTimeLabel(remainingMilliseconds: number): string {
  if (!Number.isFinite(remainingMilliseconds) || remainingMilliseconds <= 0) {
    return "审批时限已到";
  }
  const totalMinutes = Math.ceil(remainingMilliseconds / 60_000);
  if (totalMinutes < 60) {
    return `剩余 ${totalMinutes} 分钟`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0
    ? `剩余 ${hours} 小时`
    : `剩余 ${hours} 小时 ${minutes} 分钟`;
}

function headingTone(
  status: Approval["status"] | "review_required" | "idle",
): "Pending" | "Success" | "Danger" | "Neutral" {
  switch (status) {
    case "pending":
    case "reconciliation_required":
    case "review_required":
      return "Pending";
    case "executed":
      return "Success";
    case "partially_failed":
    case "failed":
    case "rejected":
    case "expired":
      return "Danger";
    case "confirmed":
    case "idle":
      return "Neutral";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function approvalHeading(
  status: Approval["status"],
  primaryCarrier: string,
): string {
  switch (status) {
    case "pending":
      return `改派至${primaryCarrier}`;
    case "confirmed":
      return "正在执行方案";
    case "reconciliation_required":
      return "方案等待平台对账";
    case "executed":
      return "方案已执行";
    case "partially_failed":
      return "方案部分执行失败";
    case "failed":
      return "方案执行失败";
    case "rejected":
      return "方案已驳回";
    case "expired":
      return "方案已过期";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function carrierDisplayName(carrierID: string, view: WaybillView | null): string {
  return (
    view?.waybill.candidate_carriers.find(
      (carrier) => carrier.carrier_id === carrierID,
    )?.name ?? carrierID
  );
}

function formatConfidence(value: number): string {
  return `${new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 1,
  }).format(value / 100)}%`;
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
    const recipientLabel =
      recipient === "shipper" ? "货主" : recipient === "driver" ? "司机" : recipient;
    const carrierID = stringParam(params, "carrier_id");
    const carrier =
      view?.waybill.candidate_carriers.find(
        (candidate) => candidate.carrier_id === carrierID,
      )?.name ?? carrierID;
    return `发送至${recipientLabel} · ${carrier}`;
  }
  return stringParam(params, "claim_type");
}

function stringParam(params: Readonly<Record<string, unknown>>, key: string): string {
  const value = params[key];
  return typeof value === "string" ? value : "--";
}

function carrierName(approval: Approval, view: WaybillView | null): string {
  const selectedCarrierID = carrierID(approval);
  return (
    view?.waybill.candidate_carriers.find(
      (carrier) => carrier.carrier_id === selectedCarrierID,
    )?.name ?? selectedCarrierID
  );
}

function carrierID(approval: Approval): string {
  const reassign = approval.items.find((item) => item.action === "tms.reassign");
  if (reassign === undefined) {
    return "候选运力";
  }
  return stringParam(reassign.params, "carrier_id");
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

function approvalStatusTone(status: Approval["status"]): HaloBadgeTone {
  switch (status) {
    case "pending":
    case "reconciliation_required":
      return "warning";
    case "confirmed":
      return "info";
    case "executed":
      return "success";
    case "partially_failed":
    case "failed":
    case "rejected":
    case "expired":
      return "danger";
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
