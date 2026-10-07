import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { InvalidRoutePage } from "./App";

vi.mock("./amap", () => ({
  hasAMapKey: () => false,
  loadAMap: () => Promise.reject(new Error("not used in route error tests")),
}));

describe("InvalidRoutePage", () => {
  it("explains the invalid URL and links back to the overview", () => {
    const markup = renderToStaticMarkup(<InvalidRoutePage />);

    expect(markup).toContain("无法打开运单工作台");
    expect(markup).toContain("URL 中的运单或任务标识无效");
    expect(markup).toContain('href="/"');
    expect(markup).toContain("返回全国经营总览");
  });
});
