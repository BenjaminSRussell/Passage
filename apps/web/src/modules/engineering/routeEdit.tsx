import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { ApiError, getRouteVersion, updateRouteVersion, type OperationRequirement, type RouteVersion, type RouteVersionOperation } from "./api/client";
import styles from "./pages.module.css";

const conflictCode = "ENG-ROUTE-VERSION-CONFLICT";
const notFoundCode = "ENG-ROUTE-NOTFOUND";
const forbiddenStatus = 403;
const operationStep = 10;
const capabilityKind = "capability";

interface OperationDraft {
  key: number;
  /** The loaded operation, so fields this form does not edit (priority, link type, queue, transit, overlap, scrap) survive the save. */
  original?: RouteVersionOperation;
  no: number;
  nextNo: number;
  operationId: string;
  setupH: string;
  runH: string;
  processQty: string;
  capability: string;
  level: string;
  /** Requirements other than the one capability this form edits. */
  otherRequirements: OperationRequirement[];
}

interface Header {
  effectiveFrom: string;
  effectiveTo: string;
}

interface Row {
  no: number;
  yours?: RouteVersionOperation;
  server?: RouteVersionOperation;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

/** Canonical text of a decimal string ("2.50", "2.5" and "02.5" are equal, ".5" is "0.5") without going through a float. */
function decimalText(value: string | undefined, fallback: string): string {
  let text = (value ?? "").trim() || fallback;
  if (text.includes(".")) text = text.replace(/0+$/, "").replace(/\.$/, "");
  text = text.replace(/^(-?)0+(?=\d)/, "$1").replace(/^(-?)\./, (_match, sign: string) => `${sign}0.`);
  return text === "" || text === "-" || text === "-0" ? "0" : text;
}

/** Every stored field of an operation, defaults filled in, so the merge view compares what a save would overwrite. */
function operationFields(op: RouteVersionOperation) {
  return {
    operationId: op.operationId ?? "",
    nextNo: String(op.nextNo ?? 0),
    linkType: op.linkType || "hard",
    priority: op.priority || "primary",
    setupH: decimalText(op.setupH, "0"),
    runH: decimalText(op.runH, "0"),
    processQty: decimalText(op.processQty, "1"),
    queueBeforeH: decimalText(op.queueBeforeH, "0"),
    queueAfterH: decimalText(op.queueAfterH, "0"),
    transitH: decimalText(op.transitH, "0"),
    efficiencyPct: decimalText(op.efficiencyPct, "0"),
    overlapQty: decimalText(op.overlapQty, "0"),
    scrapPercent: decimalText(op.scrapPercent, "0"),
    requirements: requirementsText(op.requirements),
  };
}

/** Requirements as a set: order does not matter, each one's kind, reference, level and quantity do. */
function requirementsText(requirements: OperationRequirement[] | undefined): string {
  return JSON.stringify((requirements ?? []).map((r) => [r.kind, r.refId, r.level ?? 0, r.quantity ?? 1]).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))));
}

/**
 * Every stored field of an operation in words, so a difference the table flags is always visible: the
 * fields the form edits always, the others when they are not the default.
 */
export function describeOperation(op?: RouteVersionOperation): string {
  if (!op) return "none";
  const f = operationFields(op);
  const parts = [`${f.operationId || "operation"}: setup ${f.setupH} h, run ${f.runH} h, per ${f.processQty}`];
  parts.push(f.nextNo !== "0" ? `then ${f.nextNo}` : "last");
  if (f.linkType !== "hard") parts.push(`${f.linkType} link`);
  if (f.priority !== "primary") parts.push(f.priority);
  if (f.queueBeforeH !== "0") parts.push(`queue before ${f.queueBeforeH} h`);
  if (f.queueAfterH !== "0") parts.push(`queue after ${f.queueAfterH} h`);
  if (f.transitH !== "0") parts.push(`transit ${f.transitH} h`);
  if (f.efficiencyPct !== "0") parts.push(`efficiency ${f.efficiencyPct}%`);
  if (f.overlapQty !== "0") parts.push(`overlap ${f.overlapQty}`);
  if (f.scrapPercent !== "0") parts.push(`scrap ${f.scrapPercent}%`);
  for (const r of op.requirements ?? []) parts.push(`needs ${r.quantity && r.quantity > 1 ? `${r.quantity} x ` : ""}${r.kind} ${r.refId}${r.level ? ` level ${r.level}` : ""}`);
  return parts.join(", ");
}

function same(a: RouteVersionOperation, b: RouteVersionOperation): boolean {
  const x = operationFields(a);
  const y = operationFields(b);
  return (Object.keys(x) as (keyof typeof x)[]).every((key) => x[key] === y[key]);
}

/** Operation-level merge view (BR-9.3): every operation number where your edit and the server's draft disagree. */
export function diffOperations(yours: RouteVersionOperation[], server: RouteVersionOperation[]): Row[] {
  const numbers = [...new Set([...yours, ...server].map((op) => op.no))].sort((a, b) => a - b);
  return numbers
    .map((no) => ({ no, yours: yours.find((o) => o.no === no), server: server.find((o) => o.no === no) }))
    .filter((row) => !row.yours || !row.server || !same(row.yours, row.server));
}

function headerOf(version: RouteVersion): Header {
  return { effectiveFrom: version.effectiveFrom, effectiveTo: version.effectiveTo ?? "" };
}

function headerDiffs(yours: Header, saved: Header): string[] {
  const labels: [keyof Header, string][] = [
    ["effectiveFrom", "effective from"],
    ["effectiveTo", "effective to"],
  ];
  return labels.filter(([key]) => yours[key] !== saved[key]).map(([key, name]) => `${name}: yours "${yours[key]}", saved "${saved[key]}"`);
}

/**
 * Edit screen for a draft route version (BR-9.3). Saving replaces the dates and every operation with the
 * form's content and sends the version number that was loaded; if another save came first the service
 * answers 409 and the page lists every date and operation that differs, every stored field compared.
 * The editor takes the saved value operation by operation (or the saved dates), or discards the whole
 * edit; what they keep as theirs replaces the newer draft only when they save again.
 */
export function RouteVersionEditPage({ id, onSaved, onCancel }: { id: string; onSaved?: (id: string) => void; onCancel?: () => void }) {
  const [loaded, setLoaded] = useState<RouteVersion | null>(null);
  const [loadError, setLoadError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [baseVersion, setBaseVersion] = useState(1);
  const [header, setHeader] = useState<Header>({ effectiveFrom: "", effectiveTo: "" });
  const [operations, setOperations] = useState<OperationDraft[]>([]);
  const nextKey = useRef(0);
  const idempotency = useRef({ body: "", key: "" });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [conflict, setConflict] = useState<RouteVersion | null>(null);

  function toDraft(op: RouteVersionOperation): OperationDraft {
    const own = (op.requirements ?? []).find((r) => r.kind === capabilityKind);
    return {
      key: nextKey.current++,
      original: op,
      no: op.no,
      nextNo: op.nextNo ?? 0,
      operationId: op.operationId ?? "",
      setupH: op.setupH ?? "0",
      runH: op.runH ?? "0",
      processQty: op.processQty,
      capability: own?.refId ?? "",
      level: String(own?.level ?? 1),
      otherRequirements: (op.requirements ?? []).filter((r) => r !== own),
    };
  }

  function adopt(version: RouteVersion) {
    setLoaded(version);
    setBaseVersion(version.version ?? 1);
    setHeader(headerOf(version));
    setOperations([...version.operations].sort((a, b) => a.no - b.no).map(toDraft));
    setConflict(null);
    setError("");
    setFieldErrors({});
  }

  useEffect(() => {
    let current = true;
    setLoaded(null);
    setLoadError(null);
    getRouteVersion(id)
      .then((version) => current && adopt(version))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  function update(key: number, patch: Partial<OperationDraft>) {
    setOperations((existing) => existing.map((op) => (op.key === key ? { ...op, ...patch } : op)));
  }

  /** A new operation follows the last one that ended the route, so the chain stays connected. */
  function addOperation() {
    const key = nextKey.current++;
    setOperations((existing) => {
      const no = Math.max(0, ...existing.map((op) => op.no)) + operationStep;
      const tail = [...existing].reverse().find((op) => op.nextNo === 0 && (op.original?.priority ?? "primary") === "primary");
      const linked = existing.map((op) => (op === tail ? { ...op, nextNo: no } : op));
      return [
        ...linked,
        { key, no, nextNo: 0, operationId: "", setupH: "0", runH: "0", processQty: "1", capability: "", level: "1", otherRequirements: [] },
      ];
    });
  }

  /**
   * Merge view (BR-9.3 line-level merge): replace your operation with the saved one, add the saved
   * operation you lacked, or drop the operation the saved draft removed. Links are taken as saved, so
   * the service's route checks judge the merged chain on the next save.
   */
  function takeSaved(row: Row) {
    setOperations((existing) => {
      const without = existing.filter((op) => op.no !== row.no);
      const merged = row.server ? [...without, toDraft(row.server)] : without;
      return merged.sort((a, b) => a.no - b.no);
    });
  }

  /** Removing an operation reconnects whatever pointed at it to the operation it pointed at. */
  function removeOperation(target: OperationDraft) {
    setOperations((existing) => existing.filter((op) => op.key !== target.key).map((op) => (op.nextNo === target.no ? { ...op, nextNo: target.nextNo } : op)));
  }

  function payloadOperations(): RouteVersionOperation[] {
    return operations.map((op) => ({
      ...(op.original ?? {}),
      no: op.no,
      nextNo: op.nextNo,
      operationId: op.operationId.trim(),
      setupH: op.setupH || "0",
      runH: op.runH || "0",
      processQty: op.processQty || "1",
      requirements: [
        ...op.otherRequirements,
        ...(op.capability.trim() ? [{ kind: capabilityKind, refId: op.capability.trim(), level: Number(op.level || "0"), quantity: 1 }] : []),
      ],
    }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!loaded) return;
    setError("");
    setFieldErrors({});
    setSaving(true);
    const input = {
      productId: loaded.productId,
      siteId: loaded.siteId,
      routeCode: loaded.routeCode,
      effectiveFrom: header.effectiveFrom,
      ...(header.effectiveTo ? { effectiveTo: header.effectiveTo } : {}),
      operations: payloadOperations(),
      version: baseVersion,
    };
    const body = JSON.stringify(input);
    // One key per request body and version: a retry after a lost response replays instead of conflicting with itself.
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      await updateRouteVersion(id, input, idempotency.current.key);
      onSaved?.(id);
    } catch (e) {
      if (e instanceof ApiError && e.code === conflictCode) {
        try {
          setConflict(await getRouteVersion(id));
        } catch (reload) {
          setError(errorText(reload));
        }
      } else if (e instanceof ApiError && e.status === forbiddenStatus) {
        setError("You do not have permission to change routes");
      } else {
        setError(errorText(e));
        if (e instanceof ApiError) setFieldErrors(e.errors);
      }
    } finally {
      setSaving(false);
    }
  }

  const notFound = loadError instanceof ApiError && loadError.code === notFoundCode;
  const notDraft = loaded !== null && loaded.status !== "draft";
  const rows = conflict ? diffOperations(payloadOperations(), conflict.operations) : [];
  const headerChanges = conflict ? headerDiffs(header, headerOf(conflict)) : [];
  const identical = rows.length === 0 && headerChanges.length === 0;
  const diffColumns: DataTableColumn<Row>[] = [
    { key: "no", header: "Operation", align: "end" },
    { key: "yours", header: "Your edit", render: (row) => describeOperation(row.yours) },
    { key: "server", header: "Saved by someone else", render: (row) => describeOperation(row.server) },
    {
      key: "take",
      header: "Merge",
      render: (row) => (
        <Button size="sm" onClick={() => takeSaved(row)}>
          {row.server ? `Use saved operation ${row.no}` : `Remove operation ${row.no} as saved`}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader title={loaded ? `Edit route ${loaded.routeCode}` : "Edit route draft"} description="Change the dates, operations and requirements of a draft. Active versions are copied into a new draft." />
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}
        >
          {loadError ? (notFound ? "This route version does not exist" : errorText(loadError)) : notDraft ? `Only a draft can be edited; this version is ${loaded?.status}` : error || null}
        </Alert>
      </div>
      {!loaded && !loadError ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {conflict ? (
        <Card title="Someone else saved this draft first">
          <p className={styles.lead}>
            The draft is now at version {conflict.version}; you edited version {baseVersion}.
            {identical
              ? " Your dates and operations now match it."
              : " Each difference below is replaced by your value when you save again, unless you take the saved one."}
          </p>
          {headerChanges.length > 0 ? (
            <div className={styles.toolbar}>
              <p>Dates: {headerChanges.join("; ")}</p>
              <Button size="sm" onClick={() => setHeader(headerOf(conflict))}>
                Use saved dates
              </Button>
            </div>
          ) : null}
          {rows.length > 0 ? (
            <DataTable label="Operations that differ" columns={diffColumns} rows={rows} getRowKey={(row) => String(row.no)} empty={{ title: "No differences" }} />
          ) : null}
          <div className={styles.toolbar}>
            <Button
              variant="primary"
              onClick={() => {
                setBaseVersion(conflict.version ?? baseVersion);
                setConflict(null);
              }}
            >
              {identical ? `Continue editing version ${conflict.version}` : `Keep my values and continue over version ${conflict.version}`}
            </Button>
            <Button onClick={() => adopt(conflict)}>Discard my edits and load version {conflict.version}</Button>
          </div>
        </Card>
      ) : null}
      {loaded && !notDraft && !conflict ? (
        <Card>
          <form className={styles.form} onSubmit={submit}>
            <div className={styles.field}>
              <FormField id="edit-route-from" label="Effective from" error={fieldErrors.effectiveFrom}>
                {(control) => <Input {...control} type="date" value={header.effectiveFrom} onChange={(e) => setHeader({ ...header, effectiveFrom: e.target.value })} required />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-route-to" label="Effective to" optional error={fieldErrors.effectiveTo}>
                {(control) => <Input {...control} type="date" value={header.effectiveTo} onChange={(e) => setHeader({ ...header, effectiveTo: e.target.value })} />}
              </FormField>
            </div>
            {operations.map((op, index) => (
              <Fragment key={op.key}>
                <div className={styles.field}>
                  <FormField id={`edit-route-no-${op.key}`} label={`Operation no ${index + 1}`} hint={op.original ? "Fixed: other operations link to it" : undefined} error={fieldErrors[`operations[${index}]`]}>
                    {(control) => <Input {...control} type="number" value={op.no} readOnly disabled />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-opid-${op.key}`} label={`Operation id ${index + 1}`} optional>
                    {(control) => <Input {...control} value={op.operationId} onChange={(e) => update(op.key, { operationId: e.target.value })} maxLength={20} />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-setup-${op.key}`} label={`Setup h ${index + 1}`} optional>
                    {(control) => <Input {...control} type="number" min="0" step="any" value={op.setupH} onChange={(e) => update(op.key, { setupH: e.target.value })} />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-run-${op.key}`} label={`Run h ${index + 1}`} optional>
                    {(control) => <Input {...control} type="number" min="0" step="any" value={op.runH} onChange={(e) => update(op.key, { runH: e.target.value })} />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-qty-${op.key}`} label={`Process qty ${index + 1}`} optional>
                    {(control) => <Input {...control} type="number" min="0" step="any" value={op.processQty} onChange={(e) => update(op.key, { processQty: e.target.value })} />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-cap-${op.key}`} label={`Capability ${index + 1}`} optional>
                    {(control) => <Input {...control} value={op.capability} onChange={(e) => update(op.key, { capability: e.target.value })} />}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-route-level-${op.key}`} label={`Capability level ${index + 1}`} optional>
                    {(control) => <Input {...control} type="number" min="0" step="1" value={op.level} onChange={(e) => update(op.key, { level: e.target.value })} />}
                  </FormField>
                </div>
                {operations.length > 1 ? (
                  <Button size="sm" onClick={() => removeOperation(op)}>
                    Remove operation {index + 1}
                  </Button>
                ) : null}
              </Fragment>
            ))}
            <div className={styles.toolbar}>
              <Button onClick={addOperation}>Add operation</Button>
              <Button type="submit" variant="primary" loading={saving}>Save draft</Button>
              {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
            </div>
          </form>
        </Card>
      ) : null}
      {onCancel && (notDraft || loadError) ? (
        <div className={styles.toolbar}>
          <Button size="sm" onClick={onCancel}>Back to the version</Button>
        </div>
      ) : null}
    </>
  );
}
