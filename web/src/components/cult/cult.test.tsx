import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  HaloBadge,
  HaloProgress,
  HaloSegmented,
  RollingNumber,
  TextureButton,
  TimerDisplay,
  TimerIcon,
  TimerRoot,
} from ".";

describe("Cult UI adapters", () => {
  it("keeps native button semantics inside the textured surface", () => {
    const markup = renderToStaticMarkup(
      <TextureButton type="submit" variant="destructive" disabled>
        确认驳回
      </TextureButton>,
    );

    expect(markup).toContain('data-slot="texture-button"');
    expect(markup).toContain('type="submit"');
    expect(markup).toContain("disabled");
    expect(markup).toContain("确认驳回");
  });

  it("exposes badge, number, progress, and timer values accessibly", () => {
    const markup = renderToStaticMarkup(
      <>
        <HaloBadge tone="success" live>
          在线推理
        </HaloBadge>
        <RollingNumber value={24} label="24 个任务" />
        <HaloProgress
          value={76}
          label="ETA 延误"
          showValue
          tone="high"
          ariaLabel="ETA 延误风险 76 分"
        />
        <TimerRoot title="审批时限">
          <TimerIcon />
          <TimerDisplay time="剩余 8 分钟" />
        </TimerRoot>
      </>,
    );

    expect(markup).toContain('data-slot="halo-badge"');
    expect(markup).toContain('aria-label="24 个任务"');
    expect(markup).toContain('role="progressbar"');
    expect(markup).toContain('aria-valuenow="76"');
    expect(markup).toContain('role="timer"');
    expect(markup).toContain("剩余 8 分钟");
  });

  it("renders one selected segmented option with roving tab focus", () => {
    const markup = renderToStaticMarkup(
      <HaloSegmented
        ariaLabel="队列筛选"
        items={[
          { value: "all", label: "全部" },
          { value: "active", label: "处置中" },
        ]}
        value="active"
      />,
    );

    expect(markup).toContain('data-slot="halo-segmented"');
    expect(markup).toContain('aria-label="队列筛选"');
    expect(markup).toContain('aria-pressed="true"');
    expect(markup).toContain('tabindex="0"');
    expect(markup).toContain('tabindex="-1"');
  });

  it("preserves native radio semantics when used as a filter", () => {
    const markup = renderToStaticMarkup(
      <HaloSegmented
        ariaLabel="队列筛选"
        semantics="radio"
        items={[
          { value: "all", label: "全部" },
          { value: "active", label: "处置中" },
        ]}
        value="all"
      />,
    );

    expect(markup).toContain('role="radiogroup"');
    expect(markup).toContain('type="radio"');
    expect(markup).toContain("checked");
  });
});
