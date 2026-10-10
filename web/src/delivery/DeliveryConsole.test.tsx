import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DeliveryConsoleReady } from "./DeliveryConsole";
import { deliveryWorkspaceFixture } from "./test-fixture";

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
});
