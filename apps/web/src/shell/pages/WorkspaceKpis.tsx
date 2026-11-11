import { useEffect, useMemo, useState } from "react";
import { z } from "zod";
import { ApiError, defaultFetch, requestUrl } from "../api/http";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { useWorkContext } from "../lookup/workContextState";
import { usePrincipalDuties } from "../principal";
import { Card, Skeleton, StatGroup, StatTile } from "../ui";
import { countRows, workspaceKpis, type KpiContext } from "./kpi";

type Load = { state: "ready"; body: unknown } | { state: "hidden" } | { state: "failed" };

// A list the person may not read (or that this installation does not serve) hides its tiles; any other failure
// keeps the tile and says the number is missing, so a broken service is not mistaken for a missing permission.
const HIDDEN_STATUSES = new Set([401, 403, 404]);
const anyBody = z.unknown();
const SEPARATOR = "\n";

function localToday(): string {
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

/** Reads each list once. A path with no entry yet is still loading. */
function useLists(paths: string[]): Record<string, Load> {
  const key = paths.join(SEPARATOR);
  const [loads, setLoads] = useState<{ key: string; byPath: Record<string, Load> }>({ key, byPath: {} });

  useEffect(() => {
    if (key === "") return;
    const controller = new AbortController();
    const settle = (path: string, load: Load) => {
      if (controller.signal.aborted) return;
      setLoads((prev) => ({ key, byPath: { ...(prev.key === key ? prev.byPath : {}), [path]: load } }));
    };
    for (const path of key.split(SEPARATOR)) {
      requestUrl("GET", path, anyBody, defaultFetch, {}, undefined, controller.signal).then(
        (body) => settle(path, { state: "ready", body }),
        (error: unknown) => settle(path, { state: error instanceof ApiError && HIDDEN_STATUSES.has(error.status) ? "hidden" : "failed" }),
      );
    }
    return () => controller.abort();
  }, [key]);

  return loads.key === key ? loads.byPath : {};
}

/**
 * A few numbers for the person's current workspace: invoices on hold for accounts payable, overdue order lines
 * for a buyer, journals not yet posted for a controller. Each tile counts a list the owning service already
 * serves and links to the screen with that list. A tile is left out when the person lacks the duty for its list,
 * and when the service refuses the read.
 */
export function WorkspaceKpis({ workspace }: { workspace: { id: string; label: string } | undefined }) {
  const duties = usePrincipalDuties();
  const { legalEntityId } = useWorkContext();
  const ctx = useMemo<KpiContext>(() => ({ legalEntityId, today: localToday() }), [legalEntityId]);
  const definitions = (workspace ? workspaceKpis[workspace.id] ?? [] : []).filter((d) => holdsDuty(duties, d.duty));
  const loads = useLists([...new Set(definitions.map((d) => d.path(ctx)))]);
  const shown = definitions.filter((d) => loads[d.path(ctx)]?.state !== "hidden");
  if (workspace === undefined || shown.length === 0) return null;
  const title = copy.home.kpiTitle(workspace.label);

  return (
    <Card title={title} headingLevel={2}>
      <StatGroup label={title}>
        {shown.map((d) => {
          const load = loads[d.path(ctx)];
          if (load === undefined) {
            return (
              <StatTile
                key={d.id}
                label={d.label}
                to={d.to}
                value={
                  <span aria-busy="true">
                    <span className="visually-hidden">{copy.home.kpiLoading}</span>
                    <Skeleton width="1/2" />
                  </span>
                }
              />
            );
          }
          if (load.state !== "ready") return <StatTile key={d.id} label={d.label} to={d.to} value="—" hint={copy.home.kpiUnavailable} />;
          const count = countRows(load.body, d, ctx);
          return <StatTile key={d.id} label={d.label} to={d.to} tone={d.tone} value={count.more ? `${count.value}+` : count.value} hint={count.more ? copy.home.kpiMore : undefined} />;
        })}
      </StatGroup>
    </Card>
  );
}
