import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AnimatedNumber } from "./animated-number";
import { Badge } from "./badge";
import { Button } from "./button";
import { Checkbox } from "./checkbox";
import { Progress } from "./progress";
import { Select } from "./select";
import { SegmentedControl } from "./segmented-control";
import { TimerDisplay, TimerIcon, TimerRoot } from "./timer";

describe("project UI components", () => {
  it("keeps native button semantics", () => {
    const markup = renderToStaticMarkup(
      <Button type="submit" variant="destructive" disabled>
        确认驳回
      </Button>,
    );

    expect(markup).toContain('data-slot="button"');
    expect(markup).toContain('type="submit"');
    expect(markup).toContain("disabled");
    expect(markup).toContain("确认驳回");
  });

  it("exposes badge, number, progress, and timer values accessibly", () => {
    const markup = renderToStaticMarkup(
      <>
        <Badge tone="success" live>
          在线推理
        </Badge>
        <AnimatedNumber value={24} label="24 个任务" />
        <Progress
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

    expect(markup).toContain('data-slot="badge"');
    expect(markup).toContain('aria-label="24 个任务"');
    expect(markup).toContain('role="progressbar"');
    expect(markup).toContain('aria-valuenow="76"');
    expect(markup).toContain('role="timer"');
    expect(markup).toContain("剩余 8 分钟");
  });

  it("renders one selected segmented option with roving tab focus", () => {
    const markup = renderToStaticMarkup(
      <SegmentedControl
        ariaLabel="队列筛选"
        items={[
          { value: "all", label: "全部" },
          { value: "active", label: "处置中" },
        ]}
        value="active"
      />,
    );

    expect(markup).toContain('data-slot="segmented-control"');
    expect(markup).toContain('aria-label="队列筛选"');
    expect(markup).toContain('aria-pressed="true"');
    expect(markup).toContain('tabindex="0"');
    expect(markup).toContain('tabindex="-1"');
  });

  it("preserves native radio semantics when used as a filter", () => {
    const markup = renderToStaticMarkup(
      <SegmentedControl
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

  it("exposes accessible headless selection controls", () => {
    const markup = renderToStaticMarkup(
      <>
        <Checkbox
          aria-label="选择运单"
          checked
          onCheckedChange={() => undefined}
        />
        <Select
          ariaLabel="选择公路港"
          placeholder="选择港口"
          value="hub-wuhan"
          options={[
            { value: "hub-wuhan", label: "武汉公路港" },
            { value: "hub-hangzhou", label: "杭州公路港" },
          ]}
          onValueChange={() => undefined}
        />
      </>,
    );

    expect(markup).toContain('data-slot="checkbox"');
    expect(markup).toContain('type="checkbox"');
    expect(markup).toContain("checked");
    expect(markup).toContain('data-slot="select-trigger"');
    expect(markup).toContain("<select");
    expect(markup).toContain('aria-label="选择公路港"');
  });
});
