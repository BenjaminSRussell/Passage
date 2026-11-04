import { useEffect, useState } from "react";
import { DEFAULT_MENU_ID, fetchMenu, subscribePublishes, type SubscribePublishesDeps } from "./api/client";
import { CORE_FLOWS } from "./domain/coreFlows";
import type { FlowDraft } from "./domain/flowGraph";
import { MENU_POLL_MS, publishedDrafts } from "./domain/menuSync";

export interface UseDeviceFlowsOpts {
  menuId?: string;
  fallback?: readonly FlowDraft[];
  pollMs?: number;
  EventSource?: SubscribePublishesDeps["EventSource"];
}

export interface DeviceFlows {
  flows: readonly FlowDraft[];
  source: "menu" | "bundled";
}

/**
 * Device menu (FR-32.2, AC-32.4). Loads published graphs, listens for SSE publishes, and
 * polls as a fallback so a designer change arrives within 60 s with no app update. A missing
 * mobile service keeps the bundled core flows.
 */
export function useDeviceFlows(opts: UseDeviceFlowsOpts = {}): DeviceFlows {
  const fallback = opts.fallback ?? CORE_FLOWS;
  const menuId = opts.menuId ?? DEFAULT_MENU_ID;
  const pollMs = opts.pollMs ?? MENU_POLL_MS;
  const [state, setState] = useState<DeviceFlows>({ flows: fallback, source: "bundled" });

  useEffect(() => {
    let cancelled = false;
    let next = 0;
    let applied = 0;
    const load = () => {
      const mine = ++next;
      return fetchMenu(menuId).then(
        (menu) => {
          if (cancelled || mine <= applied) return;
          applied = mine;
          setState({ flows: publishedDrafts(menu.flows), source: "menu" });
        },
        () => {
          /* Keep whatever is on the device: bundled at start, last menu after a blip. */
        },
      );
    };
    void load();
    const timer = window.setInterval(() => void load(), pollMs);
    const stop = subscribePublishes(() => void load(), { EventSource: opts.EventSource });
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      stop();
    };
  }, [menuId, pollMs, opts.EventSource]);

  return state;
}