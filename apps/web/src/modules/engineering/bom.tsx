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
  activateBomVersion,
  ApiError,
  cancelBomVersion,
  copyBomVersion,
  createBomVersion,
  getBomVersion,
  listBomVersions,
  type BomLine,
  type BomVersion,
  type BomVersionFilters,
} from "./api/client";
import { CopyVersionCard } from "./copyVersion";
import styles from "./pages.module.css";

const bomStatuses = ["draft", "active", "expired", "superseded", "cancelled"];
const emptyFilters: BomVersionFilters = { productId: "", siteId: "", status: "", asOf: "" };
const notFoundCode = "ENG-BOM-NOTFOUND";
const forbiddenStatus = 403;
const forbiddenText = "You do not have permission to change bills of material";

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

function qtyRange(version: BomVersion): string {
  return `${version.fromQty ?? "0"} to ${version.toQty ?? "open"}`;
}

const bomColumns: DataTableColumn<BomVersion>[] = [
  { key: "productId", header: "Product", mono: true },
  { key: "siteId", header: "Site", render: (row) => row.siteId || "All sites" },
  { key: "effectiveFrom", header: "Effective from" },
  { key: "effectiveTo", header: "Effective to", render: (row) => row.effectiveTo ?? "open" },
  { key: "qty", header: "Quantity range", render: qtyRange },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

export function BomVersionsPage({ onOpen, onCreate }: { onOpen?: (id: string) => void; onCreate?: () => void }) {
  const [draft, setDraft] = useState(emptyFilters);
  const [applied, setApplied] = useState(emptyFilters);
  const [rows, setRows] = useState<BomVersion[]>([]);
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
    listBomVersions(applied)
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
      const page = await listBomVersions(applied, nextCursor);
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
      <PageHeader title="Bills of material" description="BOM versions by product and site." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New BOM version
            </Button>
          ) : undefined
        }
      >
        <form className={styles.form} onSubmit={applyFilters}>
          <div className={styles.field}>
            <FormField id="bom-product" label="Product" optional>
              {(control) => <Input {...control} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="bom-site" label="Site" optional>
              {(control) => <Input {...control} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="bom-as-of" label="Effective on" optional>
              {(control) => <Input {...control} type="date" value={draft.asOf} onChange={(e) => setDraft({ ...draft, asOf: e.target.value })} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="bom-status" label="Status" optional>
              {(control) => (
                <Select {...control} value={draft.status} onChange={(e) => changeStatus(e.target.value)}>
                  <option value="">Any status</option>
                  {bomStatuses.map((status) => (
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
            label="BOM versions"
            columns={bomColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No BOM versions match" }}
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

export function BomVersionPage({
  id,
  onBack,
  onWhereUsed,
  onOpenCopy,
}: {
  id: string;
  onBack?: () => void;
  onWhereUsed?: (component: string) => void;
  onOpenCopy?: (id: string) => void;
}) {
  const [version, setVersion] = useState<BomVersion | null>(null);
  const [loadError, setLoadError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [actionError, setActionError] = useState("");
  const [announcement, setAnnouncement] = useState("");

  async function change(action: () => Promise<void>) {
    setActionError("");
    setBusy(true);
    try {
      await action();
    } catch (e) {
      setActionError(e instanceof ApiError && e.status === forbiddenStatus ? forbiddenText : errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const activate = () =>
    change(async () => {
      const activated = await activateBomVersion(id);
      setVersion(activated);
      setAnnouncement(`BOM ${activated.productId} is now active`);
    });

  const cancel = () =>
    change(async () => {
      const cancelled = await cancelBomVersion(id);
      setConfirmCancel(false);
      setVersion(cancelled);
      setAnnouncement(`BOM ${cancelled.productId} draft is cancelled`);
    });

  useEffect(() => {
    let current = true;
    setVersion(null);
    setLoadError(null);
    setActionError("");
    setAnnouncement("");
    setConfirmCancel(false);
    getBomVersion(id)
      .then((loaded) => current && setVersion(loaded))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  const notFound = loadError instanceof ApiError && loadError.code === notFoundCode;
  const facts: [string, ReactNode][] = version
    ? [
        ["Product", version.productId],
        ["Site", version.siteId || "All sites"],
        ["Effective", `${version.effectiveFrom} to ${version.effectiveTo ?? "open"}`],
        ["Quantity range", qtyRange(version)],
        ["Status", <StatusPill status={version.status} />],
        ...(version.copiedFromVersionId ? ([["Copied from", <code>{version.copiedFromVersionId}</code>]] as [string, ReactNode][]) : []),
      ]
    : [];
  const lineColumns: DataTableColumn<BomLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "componentProductId", header: "Component", mono: true },
    { key: "quantity", header: "Quantity", align: "end" },
    { key: "lineType", header: "Type" },
    { key: "scrapConst", header: "Scrap const", align: "end" },
    { key: "scrapVarPct", header: "Scrap %", align: "end" },
    { key: "roundUp", header: "Round up" },
    { key: "effectiveFrom", header: "From" },
    { key: "effectiveTo", header: "To" },
    {
      key: "where",
      header: "Where used",
      render: (line) =>
        onWhereUsed ? (
          <Button size="sm" onClick={() => onWhereUsed(line.componentProductId)}>
            Where used {line.componentProductId}
          </Button>
        ) : null,
    },
  ];

  return (
    <>
      <PageHeader title={version ? `BOM ${version.productId}` : "BOM version"} description="Components, scrap and effectivity." />
      <div className={styles.toolbar}>
        {onBack ? <Button size="sm" onClick={onBack}>Back to bills of material</Button> : null}
        {loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : null}
        {version?.status === "draft" ? (
          <>
            <Button size="sm" variant="primary" onClick={activate} disabled={busy}>
              Activate
            </Button>
            {confirmCancel ? (
              <>
                <Button size="sm" onClick={cancel} disabled={busy}>
                  Confirm cancel
                </Button>
                <Button size="sm" onClick={() => setConfirmCancel(false)} disabled={busy}>
                  Keep draft
                </Button>
              </>
            ) : (
              <Button size="sm" onClick={() => setConfirmCancel(true)} disabled={busy}>
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
          {loadError ? (notFound ? "This BOM version does not exist" : errorText(loadError)) : actionError || null}
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
            noun="BOM version"
            forbiddenText={forbiddenText}
            source={version}
            lines={(version.lines ?? []).map((line) => line.lineNo).filter((n): n is number => n !== undefined).sort((a, b) => a - b)}
            copy={(input, key) => copyBomVersion(id, input, key)}
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
              label="BOM lines"
              columns={lineColumns}
              rows={[...(version.lines ?? [])].sort((a, b) => (a.lineNo ?? 0) - (b.lineNo ?? 0))}
              getRowKey={(line) => String(line.lineNo ?? line.componentProductId)}
              empty={{ title: "No lines" }}
            />
          </div>
        </>
      ) : null}
    </>
  );
}

interface LineDraft {
  key: number;
  componentProductId: string;
  quantity: string;
  scrapVarPct: string;
}

interface CreateErrors {
  form: string;
  fields: Record<string, string>;
  lines: Record<number, string>;
}

const noErrors: CreateErrors = { form: "", fields: {}, lines: {} };
const headerFields = ["productId", "effectiveFrom", "effectiveTo", "status"];

/** Splits a COM-0001 problem into header field messages and per-line messages keyed by the line's row key. */
function createErrors(error: unknown, lines: LineDraft[]): CreateErrors {
  if (!(error instanceof ApiError)) return { ...noErrors, form: errorText(error) };
  if (error.status === forbiddenStatus) return { ...noErrors, form: "You do not have permission to create BOM versions" };
  const fields: Record<string, string> = {};
  const byLine: Record<number, string> = {};
  for (const [field, message] of Object.entries(error.errors)) {
    const line = /^lines\[(\d+)\]$/.exec(field);
    const row = line ? lines[Number(line[1])] : undefined;
    if (row) byLine[row.key] = message;
    else fields[field] = message;
  }
  const unplaced = Object.entries(fields)
    .filter(([field]) => !headerFields.includes(field))
    .map(([field, message]) => `${field} ${message}`);
  return { form: [error.message, ...unplaced].join(": "), fields, lines: byLine };
}

export function BomVersionCreatePage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [productId, setProductId] = useState("");
  const [siteId, setSiteId] = useState("");
  const [effectiveFrom, setEffectiveFrom] = useState("");
  const [effectiveTo, setEffectiveTo] = useState("");
  const [status, setStatus] = useState("draft");
  const [lines, setLines] = useState<LineDraft[]>([{ key: 0, componentProductId: "", quantity: "", scrapVarPct: "0" }]);
  const nextKey = useRef(1);
  // One key per request body: a retry of the same body after a lost response replays instead of creating a second version.
  const idempotency = useRef({ body: "", key: "" });
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<CreateErrors>(noErrors);

  function updateLine(key: number, patch: Partial<LineDraft>) {
    setLines((existing) => existing.map((line) => (line.key === key ? { ...line, ...patch } : line)));
  }

  function addLine() {
    const key = nextKey.current++;
    setLines((existing) => [...existing, { key, componentProductId: "", quantity: "", scrapVarPct: "0" }]);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setErrors(noErrors);
    setSubmitting(true);
    const submitted = lines;
    const input = {
      productId: productId.trim(),
      ...(siteId.trim() ? { siteId: siteId.trim() } : {}),
      effectiveFrom,
      ...(effectiveTo ? { effectiveTo } : {}),
      status,
      lines: submitted.map((line, index) => ({
        lineNo: index + 1,
        componentProductId: line.componentProductId.trim(),
        quantity: line.quantity,
        scrapConst: "0",
        scrapVarPct: line.scrapVarPct || "0",
      })),
    };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createBomVersion(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      setErrors(createErrors(e, submitted));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New BOM version" description="Create a draft or active bill of material." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-bom-product" label="Product" error={errors.fields.productId}>
              {(control) => <Input {...control} value={productId} onChange={(e) => setProductId(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-bom-site" label="Site" optional>
              {(control) => <Input {...control} value={siteId} onChange={(e) => setSiteId(e.target.value)} maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-bom-from" label="Effective from" error={errors.fields.effectiveFrom}>
              {(control) => <Input {...control} type="date" value={effectiveFrom} onChange={(e) => setEffectiveFrom(e.target.value)} required />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-bom-to" label="Effective to" optional error={errors.fields.effectiveTo}>
              {(control) => <Input {...control} type="date" value={effectiveTo} onChange={(e) => setEffectiveTo(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-bom-status" label="Status" error={errors.fields.status}>
              {(control) => (
                <Select {...control} value={status} onChange={(e) => setStatus(e.target.value)}>
                  <option value="draft">draft</option>
                  <option value="active">active</option>
                </Select>
              )}
            </FormField>
          </div>
          {lines.map((line, index) => (
            <Fragment key={line.key}>
              <div className={styles.field}>
                <FormField id={`new-bom-component-${line.key}`} label={`Component ${index + 1}`} error={errors.lines[line.key]}>
                  {(control) => (
                    <Input {...control} value={line.componentProductId} onChange={(e) => updateLine(line.key, { componentProductId: e.target.value })} required maxLength={64} />
                  )}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-bom-qty-${line.key}`} label={`Quantity ${index + 1}`}>
                  {(control) => (
                    <Input {...control} type="number" min="0" step="any" value={line.quantity} onChange={(e) => updateLine(line.key, { quantity: e.target.value })} required />
                  )}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-bom-scrap-${line.key}`} label={`Scrap % ${index + 1}`} optional>
                  {(control) => (
                    <Input {...control} type="number" min="0" max="100" step="any" value={line.scrapVarPct} onChange={(e) => updateLine(line.key, { scrapVarPct: e.target.value })} />
                  )}
                </FormField>
              </div>
              {lines.length > 1 ? (
                <Button size="sm" onClick={() => setLines((existing) => existing.filter((l) => l.key !== line.key))}>
                  Remove line {index + 1}
                </Button>
              ) : null}
            </Fragment>
          ))}
          <div className={styles.toolbar}>
            <Button onClick={addLine}>Add line</Button>
            <Button type="submit" variant="primary" loading={submitting}>Create BOM version</Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {errors.form || null}
        </Alert>
      </div>
    </>
  );
}
