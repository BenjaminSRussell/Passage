import { createContext, useContext, useEffect, useRef, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { holdsDuty } from "../dutyGate";
import { useWorkContext } from "../lookup/workContextState";
import { usePrincipalDuties } from "../principal";
import { INBOX_DUTY, MY_WORK_PATH, type InboxItem } from "./inbox";
import { useInbox, type InboxState } from "./useInbox";

const InboxContext = createContext<InboxState | null>(null);

/**
 * One read of the person's inbox for the whole shell, so the sidebar badge, the home page and its tiles agree and
 * the browser makes one request, not one per widget. It reads again when the person leaves the My work page, since
 * that is where items get decided.
 */
export function InboxProvider({ children }: { children: ReactNode }) {
  const { legalEntityId } = useWorkContext();
  const enabled = holdsDuty(usePrincipalDuties(), INBOX_DUTY);
  const inbox = useInbox(legalEntityId, enabled);
  const { pathname } = useLocation();
  const previous = useRef(pathname);
  const { reload } = inbox;

  useEffect(() => {
    const left = previous.current.startsWith(MY_WORK_PATH) && !pathname.startsWith(MY_WORK_PATH);
    previous.current = pathname;
    if (left && enabled) reload();
  }, [pathname, enabled, reload]);

  return <InboxContext.Provider value={enabled ? inbox : null}>{children}</InboxContext.Provider>;
}

/** The shared inbox, or null when the person holds no inbox duty. */
export function useSharedInbox(): InboxState | null {
  return useContext(InboxContext);
}

export interface InboxCounts {
  total: number;
  /** True when more is waiting than was read, so the total is a floor. */
  more: boolean;
  approvals: number;
  overdue: number;
  /** Whole days the oldest waiting item has waited, or 0 with none. */
  oldestDays: number;
}

const DAY_MS = 86_400_000;

export function countInbox(items: InboxItem[], more: boolean, now: Date = new Date()): InboxCounts {
  const today = now.toISOString().slice(0, 10);
  let oldest = now.getTime();
  for (const item of items) {
    const created = Date.parse(item.createdAt);
    if (!Number.isNaN(created) && created < oldest) oldest = created;
  }
  return {
    total: items.length,
    more,
    approvals: items.filter((i) => i.actions.includes("approve")).length,
    overdue: items.filter((i) => i.dueDate !== undefined && i.dueDate < today).length,
    oldestDays: items.length === 0 ? 0 : Math.max(0, Math.floor((now.getTime() - oldest) / DAY_MS)),
  };
}

/** The count shown on the badge: "7", or "50+" when more is waiting than was read. */
export function badgeText(counts: InboxCounts): string {
  return `${counts.total}${counts.more ? "+" : ""}`;
}
