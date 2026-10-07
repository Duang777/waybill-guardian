export type OverviewChartTheme = {
  paper: string;
  ink: string;
  muted: string;
  line: string;
  palette: readonly [string, ...string[]];
};

export function readOverviewChartTheme(
  element: HTMLElement,
): OverviewChartTheme {
  const style = window.getComputedStyle(element);
  return {
    paper: token(style, "--chart-paper", "#fdfcf8"),
    ink: token(style, "--chart-ink", "#282b2e"),
    muted: token(style, "--chart-muted", "#687078"),
    line: token(style, "--chart-line", "#d9dcda"),
    palette: [
      token(style, "--chart-signal", "#c84c2d"),
      token(style, "--chart-network", "#28756f"),
      token(style, "--chart-gold", "#a67924"),
      token(style, "--chart-blue", "#47789a"),
      token(style, "--chart-green", "#4f7d55"),
      token(style, "--chart-rose", "#a85b66"),
      token(style, "--chart-graphite", "#5e6469"),
    ],
  };
}

function token(
  style: CSSStyleDeclaration,
  name: string,
  fallback: string,
): string {
  return style.getPropertyValue(name).trim() || fallback;
}
