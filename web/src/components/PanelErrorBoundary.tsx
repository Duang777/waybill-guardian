import { RotateCw } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui/button";
import { StateFeedback } from "./StateFeedback";

type PanelErrorBoundaryProps = {
  name: string;
  children: ReactNode;
};

type PanelErrorBoundaryState =
  | { kind: "ready" }
  | { kind: "failed"; detail: string };

export class PanelErrorBoundary extends Component<
  PanelErrorBoundaryProps,
  PanelErrorBoundaryState
> {
  state: PanelErrorBoundaryState = { kind: "ready" };

  static getDerivedStateFromError(error: Error): PanelErrorBoundaryState {
    return { kind: "failed", detail: error.message };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("panel render failed", {
      panel: this.props.name,
      error,
      componentStack: info.componentStack,
    });
  }

  render() {
    if (this.state.kind === "ready") {
      return this.props.children;
    }
    return (
      <StateFeedback
        tone="error"
        eyebrow="Panel error"
        title={`${this.props.name}暂不可用`}
        detail={this.state.detail}
        compact
        action={
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => this.setState({ kind: "ready" })}
          >
            <RotateCw aria-hidden="true" size={15} />
            重试
          </Button>
        }
      />
    );
  }
}
