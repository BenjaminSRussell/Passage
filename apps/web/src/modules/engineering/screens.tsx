import { Fragment, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  activateRouteVersion,
  ApiError,
  cancelRouteVersion,
  computeLeadTime,
  copyRouteVersion,
  getRouteVersion,
  listRouteVersions,
  type LeadTime,
  type OperationRequirement,
  type RouteVersion,
  type RouteVersionFilters,
  type RouteVersionHeader,
  type RouteVersionOperation,
} from "./api/client";
import { CopyVersionCard } from "./copyVersion";
import styles from "./pages.module.css";

const routeStatuses = ["draft", "active", "expired", "cancelled"];

const routeColumns: DataTableColumn<RouteVersionHeader>[] = [
  { key: "routeCode", header: "Route code", mono: true },
  { key: "productId", header: "Product", mono: true },
  { key: "siteId", header: "Site" },
  { key: "versionNo", header: "Version", align: "end" },
  { key: "effectiveFrom", header: "Effective from" },
  { key: "effectiveTo", header: "Effective to" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "operationCount", header: "Operations", align: "end" },
];

const filterInputs = [
  { key: "productId", id: "route-product", label: "Product", type: "text" },
  { key: "siteId", id: "route-site", label: "Site", type: "text" },
  { key: "asOf", id: "route-as-of", label: "As of date", type: "date" },
] as const;

const emptyFilters: RouteVersionFilters = { productId: "", siteId: "", status: "", asOf: "" };

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

export function RouteVersionsPage({ onOpen, onCreate }: { onOpen?: (id: string) => void; onCreate?: () => void }) {
  const [draft, setDraft] = useState(emptyFilters);
  const [applied, setApplied] = useState(emptyFilters);
  const [rows, setRows] = useState<RouteVersionHeader[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    let current = true;
    generation.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError("");
    listRouteVersions(applied)
      .then((page) => {
        if (!current) return;
        setRows(page.items);
        setNextCursor(page.nextCursor ?? "");
      })
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setNextCursor("");
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [applied, attempt]);

  async function loadMore() {
    const token = generation.current;
    setLoadingMore(true);
    setError("");
    try {
      const page = await listRouteVersions(applied, nextCursor);
      if (token !== generation.current) return;
      setRows((existing) => [...existing, ...page.items]);
      setNextCursor(page.nextCursor ?? "");
    } catch (e) {
      if (token === generation.current) setError(errorText(e));
    } finally {
      if (token === generation.current) setLoadingMore(false);
    }
  }

  function changeStatus(status: string) {
    setDraft((existing) => ({ ...existing, status }));
    setApplied((existing) => ({ ...existing, status }));
  }

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setApplied((existing) => ({ ...draft, status: existing.status }));
  }

  return (
    <>
      <PageHeader title="Routings" description="Route versions by product and site." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New route version
            </Button>
          ) : undefined
        }
      >
        <form className={styles.form} onSubmit={applyFilters}>
          {filterInputs.map(({ key, id, label, type }) => (
            <div key={key} className={styles.field}>
              <FormField id={id} label={label} optional>
                {(control) => <Input {...control} type={type} value={draft[key]} onChange={(e) => setDraft({ ...draft, [key]: e.target.value })} />}
              </FormField>
            </div>
          ))}
          <div className={styles.field}>
            <FormField id="route-status" label="Status" optional>
              {(control) => (
                <Select {...control} value={draft.status} onChange={(e) => changeStatus(e.target.value)}>
                  <option value="">Any status</option>
                  {routeStatuses.map((status) => (
                    <option key={status} value={status}>
                      {status}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <Button type="submit" variant="primary">Apply filters</Button>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}
        >
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Route versions"
            columns={routeColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No route versions match" }}
            onRowOpen={(row) => onOpen?.(row.id)}
          />
        )}
      </div>
      {nextCursor !== "" && !loading ? (
        <div className={styles.toolbar}>
          <Button loading={loadingMore} onClick={loadMore}>Load more</Button>
        </div>
      ) : null}
    </>
  );
}

function requirementText(requirement: OperationRequirement): string {
  const parts = [`${requirement.kind} ${requirement.refId}`];
  if (requirement.level !== undefined) parts.push(`level ${requirement.level}`);
  if (requirement.quantity !== undefined) parts.push(`quantity ${requirement.quantity}`);
  return parts.join(", ");
}

const operationColumns: DataTableColumn<RouteVersionOperation>[] = [
  { key: "no", header: "No", align: "end" },
  { key: "operationId", header: "Operation", mono: true },
  { key: "nextNo", header: "Next no", align: "end" },
  { key: "linkType", header: "Link" },
  { key: "priority", header: "Priority" },
  { key: "setupH", header: "Setup h", align: "end" },
  { key: "runH", header: "Run h", align: "end" },
  { key: "queueBeforeH", header: "Queue h", align: "end", render: (op) => `${op.queueBeforeH ?? 0} / ${op.queueAfterH ?? 0}` },
  { key: "transitH", header: "Transit h", align: "end" },
  { key: "efficiencyPct", header: "Efficiency %", align: "end" },
  {
    key: "requirements",
    header: "Requirements",
    render: (op) => (
      <ul className={styles.requirements}>
        {(op.requirements ?? []).map((requirement, index) => (
          <li key={index}>{requirementText(requirement)}</li>
        ))}
      </ul>
    ),
  },
];

const notFoundCode = "ENG-ROUTE-NOTFOUND";
const forbiddenStatus = 403;
const activateForbiddenText = "You do not have permission to change routes";

export function RouteVersionPage({
  id,
  onBack,
  onOpenCopy,
  onEdit,
}: {
  id: string;
  onBack?: () => void;
  onOpenCopy?: (id: string) => void;
  onEdit?: (id: string) => void;
}) {
  const [version, setVersion] = useState<RouteVersion | null>(null);
  const [loadError, setLoadError] = useState<ApiError | Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [quantity, setQuantity] = useState("");
  const [hoursPerDay, setHoursPerDay] = useState("");
  const [calculating, setCalculating] = useState(false);
  const [leadTime, setLeadTime] = useState<LeadTime | null>(null);
  const [leadError, setLeadError] = useState("");
  const [activating, setActivating] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [activateError, setActivateError] = useState("");
  const [announcement, setAnnouncement] = useState("");

  useEffect(() => {
    let current = true;
    setVersion(null);
    setActivateError("");
    setAnnouncement("");
    setConfirmCancel(false);
    setLoadError(null);
    getRouteVersion(id)
      .then((loaded) => current && setVersion(loaded))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  const leadFields = [
    { id: "lead-time-quantity", label: "Quantity", value: quantity, set: setQuantity },
    { id: "lead-time-hours-per-day", label: "Hours per day", value: hoursPerDay, set: setHoursPerDay },
  ];
  const facts: [string, ReactNode][] = version
    ? [
        ["Route code", version.routeCode],
        ["Product", version.productId],
        ["Site", version.siteId],
        ["Version", version.versionNo],
        ["Effective", `${version.effectiveFrom} to ${version.effectiveTo ?? "open"}`],
        ["Status", <StatusPill status={version.status} />],
        ...(version.status === "draft" && version.version ? ([["Draft version", String(version.version)]] as [string, ReactNode][]) : []),
        ...(version.copiedFromVersionId ? ([["Copied from", <code>{version.copiedFromVersionId}</code>]] as [string, ReactNode][]) : []),
      ]
    : [];
  const operations = version ? [...version.operations].sort((a, b) => a.no - b.no) : [];
  const notFound = loadError instanceof ApiError && loadError.code === notFoundCode;
  const alertText = loadError ? (notFound ? "This route version does not exist" : errorText(loadError)) : activateError || leadError;

  async function activate() {
    setActivateError("");
    setAnnouncement("");
    setActivating(true);
    try {
      const activated = await activateRouteVersion(id);
      setVersion(activated);
      setAnnouncement(`Route ${activated.routeCode} version ${activated.versionNo} is now active`);
    } catch (e) {
      setActivateError(e instanceof ApiError && e.status === forbiddenStatus ? activateForbiddenText : errorText(e));
    } finally {
      setActivating(false);
    }
  }

  async function cancel() {
    setActivateError("");
    setAnnouncement("");
    setActivating(true);
    try {
      const cancelled = await cancelRouteVersion(id);
      setConfirmCancel(false);
      setVersion(cancelled);
      setAnnouncement(`Route ${cancelled.routeCode} version ${cancelled.versionNo} draft is cancelled`);
    } catch (e) {
      setActivateError(e instanceof ApiError && e.status === forbiddenStatus ? activateForbiddenText : errorText(e));
    } finally {
      setActivating(false);
    }
  }

  async function calculate(event: FormEvent) {
    event.preventDefault();
    setLeadError("");
    setLeadTime(null);
    setCalculating(true);
    try {
      setLeadTime(
        await computeLeadTime({
          operations: operations.map((op) => ({
            no: op.no,
            next: op.nextNo,
            priority: op.priority,
            setup: op.setupH,
            run: op.runH,
            processQty: op.processQty,
            queueBefore: op.queueBeforeH,
            queueAfter: op.queueAfterH,
            transit: op.transitH,
            efficiencyPct: op.efficiencyPct,
          })),
          quantity,
          hoursPerDay,
        }),
      );
    } catch (e) {
      setLeadError(errorText(e));
    } finally {
      setCalculating(false);
    }
  }

  return (
    <>
      <PageHeader title={version ? `Route ${version.routeCode}` : "Route version"} description="Operations, requirements and lead time." />
      <div className={styles.toolbar}>
        {onBack ? <Button size="sm" onClick={onBack}>Back to routings</Button> : null}
        {loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : null}
        {version?.status === "draft" ? (
          <>
            {onEdit ? (
              <Button size="sm" onClick={() => onEdit(id)} disabled={activating}>
                Edit draft
              </Button>
            ) : null}
            <Button size="sm" onClick={activate} disabled={activating}>
              Activate
            </Button>
            {confirmCancel ? (
              <>
                <Button size="sm" onClick={cancel} disabled={activating}>
                  Confirm cancel
                </Button>
                <Button size="sm" onClick={() => setConfirmCancel(false)} disabled={activating}>
                  Keep draft
                </Button>
              </>
            ) : (
              <Button size="sm" onClick={() => setConfirmCancel(true)} disabled={activating}>
                Cancel draft
              </Button>
            )}
          </>
        ) : null}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {announcement}
      </p>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {alertText || null}
        </Alert>
      </div>
      {!version && !loadError ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {version ? (
        <>
          <CopyVersionCard
            key={id}
            noun="route version"
            forbiddenText={activateForbiddenText}
            source={version}
            siteRequired
            copy={(input, key) => copyRouteVersion(id, input, key)}
            onCopied={onOpenCopy}
          />
          <Card title="Version">
            <dl className={styles.facts}>
              {facts.map(([term, value]) => (
                <Fragment key={term}>
                  <dt>{term}</dt>
                  <dd>{value}</dd>
                </Fragment>
              ))}
            </dl>
          </Card>
          <div className={styles.results}>
            <DataTable
              label="Operations"
              columns={operationColumns}
              rows={operations}
              getRowKey={(op) => String(op.no)}
              empty={{ title: "No operations" }}
            />
          </div>
          <Card title="Lead time">
            <form className={styles.form} onSubmit={calculate}>
              {leadFields.map(({ id, label, value, set }) => (
                <div key={id} className={styles.field}>
                  <FormField id={id} label={label}>
                    {(control) => <Input {...control} type="number" min="0" step="any" value={value} onChange={(e) => set(e.target.value)} required />}
                  </FormField>
                </div>
              ))}
              <Button type="submit" variant="primary" loading={calculating}>Calculate</Button>
            </form>
            <p role="status" className={styles.summary}>
              {leadTime ? `${leadTime.hours} hours, ${leadTime.calendarDays} calendar days` : ""}
            </p>
          </Card>
        </>
      ) : null}
    </>
  );
}
