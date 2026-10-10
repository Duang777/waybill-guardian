import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DeliveryConsoleReady } from "./DeliveryConsole";
import {
  deliveryWorkspaceFixture,
  deliveryWorkspaceStateFixture,
} from "./test-fixture";

describe("DeliveryConsoleReady", () => {
  it("renders plan facts, synchronized views, approval effects, and audit", () => {
    const markup = renderToStaticMarkup(
      <DeliveryConsoleReady workspace={deliveryWorkspaceFixture()} />,
    );

    expect(markup).toContain("杭州城市配送计划");
    expect(markup).toContain("路线与站序");
    expect(markup).toContain("逐站装卸");
    expect(markup).toContain("司机与能源");
    expect(markup).toContain("100.0%");
    expect(markup).toContain("WebGL 降级视图");
    expect(markup).toContain("tms.bind_dispatch_plan");
    expect(markup).toContain("wms.publish_loading_instruction");
    expect(markup).toContain("计划修订差异");
    expect(markup).toContain("REV-HZ-1010-06");
    expect(markup).toContain("总里程");
    expect(markup).toContain("执行与对账");
    expect(markup).toContain("确认并执行");
    expect(markup).toContain("独立 Validator 确认硬约束违规为零");
  });

  it("exposes keyboard and DOM controls for every route step", () => {
    const markup = renderToStaticMarkup(
      <DeliveryConsoleReady workspace={deliveryWorkspaceFixture()} />,
    );

    expect(markup.match(/aria-current="step"/g)).toHaveLength(2);
    expect(markup).toContain("选择第 1 站");
    expect(markup).toContain("杭州传化公路港");
    expect(markup).toContain("萧山产业园");
    expect(markup).toContain("绍兴柯桥仓");
    expect(markup).toContain("余杭制造基地");
  });

  it("renders partial and reconciliation outcomes without offering client actions", () => {
    const partialMarkup = renderToStaticMarkup(
      <DeliveryConsoleReady
        workspace={deliveryWorkspaceStateFixture("partial")}
      />,
    );
    const reconciliationMarkup = renderToStaticMarkup(
      <DeliveryConsoleReady
        workspace={deliveryWorkspaceStateFixture("reconciliation")}
      />,
    );

    expect(partialMarkup).toContain("部分写入");
    expect(partialMarkup).toContain("已完成");
    expect(partialMarkup).toContain("失败");
    expect(reconciliationMarkup).toContain("等待对账");
    expect(reconciliationMarkup).toContain("外部请求超时");
    expect(reconciliationMarkup).toContain("wms-prod");
    expect(reconciliationMarkup).not.toContain("立即重试");
  });

  it("locks approval controls while the revision stream reconnects", () => {
    const markup = renderToStaticMarkup(
      <DeliveryConsoleReady
        workspace={deliveryWorkspaceFixture()}
        connection="reconnecting"
      />,
    );

    expect(markup).toContain("实时流重连中");
    expect(markup).toContain("系统已锁定审批操作");
    expect(markup.match(/disabled=""/g)?.length).toBeGreaterThanOrEqual(2);
  });
});
