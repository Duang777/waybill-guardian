import { useCallback, useEffect, useState } from "react";
import {
  getDeliveryWorkspace,
  openDeliveryWorkspaceEvents,
  type DeliveryStreamConnectionState,
} from "./api";
import {
  deliveryConnectionState,
  type DeliveryConnectionState,
} from "./connection";
import type {
  DeliveryWorkspace,
  PlanRevisionID,
} from "./contract";
import { toRequestIssue, type RequestIssue } from "../request-issue";

export type DeliveryWorkspaceResource =
  | { kind: "loading" }
  | { kind: "ready"; workspace: DeliveryWorkspace }
  | { kind: "error"; issue: RequestIssue };

export function useDeliveryWorkspace(
  revisionID: PlanRevisionID,
): {
  resource: DeliveryWorkspaceResource;
  connection: DeliveryConnectionState;
  reload: () => void;
} {
  const [resource, setResource] = useState<DeliveryWorkspaceResource>({
    kind: "loading",
  });
  const [reloadKey, setReloadKey] = useState(0);
  const [transport, setTransport] =
    useState<DeliveryStreamConnectionState>("closed");
  const [browserOnline, setBrowserOnline] = useState(() =>
    typeof navigator === "undefined" ? true : navigator.onLine,
  );

  useEffect(() => {
    const online = () => setBrowserOnline(true);
    const offline = () => setBrowserOnline(false);
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
    return () => {
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    let closeStream: (() => void) | null = null;
    let refreshInFlight = false;
    let refreshQueued = false;
    let refreshTimer: number | null = null;
    let latestEventSeq = 0;
    let streamState: DeliveryStreamConnectionState = "connecting";
    setResource({ kind: "loading" });
    setTransport("connecting");

    function scheduleRefresh(): void {
      if (refreshTimer !== null || !active) {
        return;
      }
      refreshTimer = window.setTimeout(() => {
        refreshTimer = null;
        void refresh();
      }, 500);
    }

    async function refresh(): Promise<void> {
      if (refreshInFlight) {
        refreshQueued = true;
        return;
      }
      refreshInFlight = true;
      try {
        do {
          refreshQueued = false;
          const workspace = await getDeliveryWorkspace(
            revisionID,
            controller.signal,
          );
          if (!active) {
            return;
          }
          if (workspace.last_event_seq < latestEventSeq) {
            setTransport("reconnecting");
            scheduleRefresh();
            continue;
          }
          setResource({ kind: "ready", workspace });
          setTransport(streamState);
          if (isSealedRunState(workspace.run_state)) {
            closeStream?.();
          }
        } while (refreshQueued);
      } catch (error) {
        if (
          active &&
          !(error instanceof DOMException && error.name === "AbortError")
        ) {
          setTransport("reconnecting");
          scheduleRefresh();
        }
      } finally {
        refreshInFlight = false;
      }
    }

    const bootstrap = async (): Promise<void> => {
      try {
        const workspace = await getDeliveryWorkspace(
          revisionID,
          controller.signal,
        );
        if (!active) {
          return;
        }
        latestEventSeq = workspace.last_event_seq;
        setResource({ kind: "ready", workspace });
        if (isSealedRunState(workspace.run_state)) {
          setTransport("closed");
          return;
        }
        closeStream = openDeliveryWorkspaceEvents(
          revisionID,
          workspace.last_event_seq,
          {
            onEvent: (event) => {
              latestEventSeq = Math.max(latestEventSeq, event.seq);
              void refresh();
            },
            onConnectionChange: (state) => {
              streamState = state;
              if (active) {
                setTransport(state);
              }
            },
            onError: () => {
              if (active) {
                setTransport("reconnecting");
              }
            },
          },
        );
      } catch (error) {
        if (
          !active ||
          (error instanceof DOMException && error.name === "AbortError")
        ) {
          return;
        }
        setTransport("closed");
        setResource({ kind: "error", issue: toRequestIssue(error) });
      }
    };

    void bootstrap();
    return () => {
      active = false;
      controller.abort();
      if (refreshTimer !== null) {
        window.clearTimeout(refreshTimer);
      }
      closeStream?.();
    };
  }, [reloadKey, revisionID]);

  const reload = useCallback(() => {
    setReloadKey((value) => value + 1);
  }, []);
  const runState =
    resource.kind === "ready" ? resource.workspace.run_state : null;

  return {
    resource,
    connection: deliveryConnectionState({
      runState,
      transport,
      browserOnline,
    }),
    reload,
  };
}

function isSealedRunState(
  runState: DeliveryWorkspace["run_state"],
): boolean {
  return (
    runState === "completed" ||
    runState === "failed" ||
    runState === "manual_review"
  );
}
