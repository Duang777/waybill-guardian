const anomalyTypeLabels: Readonly<Record<string, string>> = {
  delay: "时效延误",
  damage: "货损",
  fatigue: "疲劳驾驶",
  loss: "货物丢失",
  weather: "天气影响",
};

export function anomalyTypeLabel(value: string): string {
  return anomalyTypeLabels[value] ?? value;
}
