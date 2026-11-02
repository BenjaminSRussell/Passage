import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { ApiError, bomCategories, bomUsages, getBomVersion, updateBomVersion, type BomLine, type BomVersion } from "./api/client";
import styles from "./pages.module.css";

const conflictCode = "ENG-BOM-VERSION-CONFLICT";
const notFoundCode = "ENG-BOM-NOTFOUND";
const forbiddenStatus = 403;

interface LineDraft {
  key: number;
  /** The loaded line, so fields this form does not edit (series, type, round-up, dates) survive the save. */
  original?: BomLine;
  lineNo: number;
  componentProductId: string;
  quantity: string;
  scrapVarPct: string;
  /** FR-9.20 alternative group; empty for a line outside any group. */
  altGroup: string;
  altStrategy: string;
  /** Priority (1 to 99) with the priority strategy, percent with usage_probability. */
  altValue: string;
}

interface Header {
  usage: string;
  category: string;
  siteId: string;
  effectiveFrom: string;
  effectiveTo: string;
  fromQty: string;
  toQty: string;
}

interface Row {
  lineNo: number;
  yours?: BomLine;
  server?: BomLine;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

/** Canonical text of a decimal string ("2.50" and "2.5" are equal) without going through a float. */
function decimalText(value: string | undefined, fallback: string): string {
  let text = (value ?? "").trim() || fallback;
  if (text.includes(".")) text = text.replace(/0+$/, "").replace(/\.$/, "");
  return text.replace(/^(-?)0+(?=\d)/, "$1");
}

/** Every stored field of a line, defaults filled in, so the merge view compares what a save would overwrite. */
function lineFields(line: BomLine) {
  return {
    component: line.componentProductId,
    quantity: decimalText(line.quantity, "0"),
    scrapVarPct: decimalText(line.scrapVarPct, "0"),
    scrapConst: decimalText(line.scrapConst, "0"),
    series: decimalText(line.series, "1"),
    lineType: line.lineType || "item",
    roundUp: line.roundUp ?? "",
    effectiveFrom: line.effectiveFrom ?? "",
    effectiveTo: line.effectiveTo ?? "",
    altGroup: line.altGroup ?? "",
    altStrategy: line.altStrategy ?? "",
    altPriority: String(line.altPriority ?? ""),
    usageProbabilityPct: decimalText(line.usageProbabilityPct, ""),
    discontinuation: [line.discontinuationGroup, line.discontinueType, line.followUpGroup, line.followUpComponentId, line.discontinueOn].map((v) => v ?? "").join("|"),
  };
}

function describe(line?: BomLine): string {
  if (!line) return "none";
  const f = lineFields(line);
  const parts = [`${f.component} x ${line.quantity}, scrap ${line.scrapVarPct}%`];
  if (f.scrapConst !== "0") parts.push(`constant scrap ${line.scrapConst}`);
  if (f.series !== "1") parts.push(`per ${line.series}`);
  if (f.lineType !== "item") parts.push(f.lineType);
  if (f.roundUp) parts.push(`round up ${f.roundUp}`);
  if (f.effectiveFrom || f.effectiveTo) parts.push(`effective ${f.effectiveFrom || "start"} to ${f.effectiveTo || "open"}`);
  if (f.altGroup) parts.push(`alternative group ${f.altGroup} (${f.altStrategy} ${f.altPriority || f.usageProbabilityPct})`);
  if (line.discontinueType) parts.push(`discontinued ${line.discontinueOn ?? "undated"}, ${line.discontinueType}${line.followUpComponentId ? `, follow-up ${line.followUpComponentId}` : ""}`);
  return parts.join(", ");
}

function same(a: BomLine, b: BomLine): boolean {
  const x = lineFields(a);
  const y = lineFields(b);
  return (Object.keys(x) as (keyof typeof x)[]).every((key) => x[key] === y[key]);
}

/** Line-level merge view (BR-9.3): every line number where your edit and the server's draft disagree. */
export function diffLines(yours: BomLine[], server: BomLine[]): Row[] {
  const lineNos = [...new Set([...yours, ...server].map((line) => line.lineNo ?? 0))].sort((a, b) => a - b);
  return lineNos
    .map((lineNo) => ({ lineNo, yours: yours.find((l) => l.lineNo === lineNo), server: server.find((l) => l.lineNo === lineNo) }))
    .filter((row) => !row.yours || !row.server || !same(row.yours, row.server));
}

/** The group fields a save sends for a line: the group the form shows, never a stale priority or probability of the other strategy. */
function altFields(line: LineDraft): Partial<BomLine> {
  const altGroup = line.altGroup.trim();
  if (!altGroup) return { altGroup: undefined, altStrategy: undefined, altPriority: undefined, usageProbabilityPct: undefined };
  return line.altStrategy === "usage_probability"
    ? { altGroup, altStrategy: line.altStrategy, altPriority: undefined, usageProbabilityPct: line.altValue }
    : { altGroup, altStrategy: "priority", altPriority: Number(line.altValue), usageProbabilityPct: undefined };
}

function headerOf(version: BomVersion): Header {
  return {
    usage: version.usage || "production",
    category: version.category || "material",
    siteId: version.siteId ?? "",
    effectiveFrom: version.effectiveFrom,
    effectiveTo: version.effectiveTo ?? "",
    fromQty: version.fromQty ?? "",
    toQty: version.toQty ?? "",
  };
}

const headerLabels: [keyof Header, string][] = [
  ["usage", "usage"],
  ["category", "category"],
  ["siteId", "site"],
  ["effectiveFrom", "effective from"],
  ["effectiveTo", "effective to"],
  ["fromQty", "from quantity"],
  ["toQty", "to quantity"],
];

function headerDiffs(yours: Header, saved: Header): string[] {
  return headerLabels
    .filter(([key]) => (key === "fromQty" || key === "toQty" ? decimalText(yours[key], "") !== decimalText(saved[key], "") : yours[key].trim() !== saved[key]))
    .map(([key, name]) => `${name}: yours "${yours[key].trim()}", saved "${saved[key]}"`);
}

/**
 * Edit screen for a draft BOM version (BR-9.3). Saving replaces the header and every line with the
 * form's content and sends the version number that was loaded; if another save came first the service
 * answers 409 and the page shows every header field and line that differs, field by field. The editor
 * takes the saved value line by line (or the saved header), or discards the whole edit; what they keep
 * as theirs replaces the newer draft only when they save again, so nothing is overwritten unseen.
 */
export function BomVersionEditPage({ id, onSaved, onCancel }: { id: string; onSaved?: (id: string) => void; onCancel?: () => void }) {
  const [loaded, setLoaded] = useState<BomVersion | null>(null);
  const [loadError, setLoadError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [baseVersion, setBaseVersion] = useState(1);
  const [header, setHeader] = useState<Header>({ usage: "production", category: "material", siteId: "", effectiveFrom: "", effectiveTo: "", fromQty: "", toQty: "" });
  const [lines, setLines] = useState<LineDraft[]>([]);
  const nextKey = useRef(0);
  const nextLineNo = useRef(1);
  const idempotency = useRef({ body: "", key: "" });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [conflict, setConflict] = useState<BomVersion | null>(null);

  function toDraft(line: BomLine): LineDraft {
    return {
      key: nextKey.current++,
      original: line,
      lineNo: line.lineNo ?? 0,
      componentProductId: line.componentProductId,
      quantity: line.quantity,
      scrapVarPct: line.scrapVarPct,
      altGroup: line.altGroup ?? "",
      altStrategy: line.altStrategy || "priority",
      altValue: line.altStrategy === "usage_probability" ? (line.usageProbabilityPct ?? "") : line.altPriority ? String(line.altPriority) : "",
    };
  }

  function adopt(version: BomVersion) {
    setLoaded(version);
    setBaseVersion(version.version ?? 1);
    setHeader(headerOf(version));
    const drafts = [...(version.lines ?? [])].sort((a, b) => (a.lineNo ?? 0) - (b.lineNo ?? 0)).map(toDraft);
    nextLineNo.current = Math.max(nextLineNo.current, ...drafts.map((d) => d.lineNo + 1), 1);
    setLines(drafts);
    setConflict(null);
    setError("");
    setFieldErrors({});
  }

  useEffect(() => {
    let current = true;
    setLoaded(null);
    setLoadError(null);
    getBomVersion(id)
      .then((version) => current && adopt(version))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  function updateLine(key: number, patch: Partial<LineDraft>) {
    setLines((existing) => existing.map((line) => (line.key === key ? { ...line, ...patch } : line)));
  }

  function addLine() {
    const key = nextKey.current++;
    const lineNo = nextLineNo.current++;
    setLines((existing) => [...existing, { key, lineNo, componentProductId: "", quantity: "", scrapVarPct: "0", altGroup: "", altStrategy: "priority", altValue: "" }]);
  }

  /** Merge view: replace your line with the saved one, add the saved line you lacked, or drop the line the saved draft removed. */
  function takeSaved(row: Row) {
    setLines((existing) => {
      const without = existing.filter((line) => line.lineNo !== row.lineNo);
      const merged = row.server ? [...without, toDraft(row.server)] : without;
      return merged.sort((a, b) => a.lineNo - b.lineNo);
    });
    nextLineNo.current = Math.max(nextLineNo.current, row.lineNo + 1);
  }

  function payloadLines(): BomLine[] {
    return lines.map((line) => ({
      ...(line.original ?? { scrapConst: "0" }),
      lineNo: line.lineNo,
      componentProductId: line.componentProductId.trim(),
      quantity: line.quantity,
      scrapVarPct: line.scrapVarPct || "0",
      ...altFields(line),
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
      usage: header.usage,
      category: header.category,
      siteId: header.siteId.trim(),
      effectiveFrom: header.effectiveFrom,
      ...(header.effectiveTo ? { effectiveTo: header.effectiveTo } : {}),
      ...(header.fromQty ? { fromQty: header.fromQty } : {}),
      ...(header.toQty ? { toQty: header.toQty } : {}),
      lines: payloadLines(),
      version: baseVersion,
    };
    const body = JSON.stringify(input);
    // One key per request body and version: a retry after a lost response replays instead of conflicting with itself.
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      await updateBomVersion(id, input, idempotency.current.key);
      onSaved?.(id);
    } catch (e) {
      if (e instanceof ApiError && e.code === conflictCode) {
        try {
          setConflict(await getBomVersion(id));
        } catch (reload) {
          setError(errorText(reload));
        }
      } else if (e instanceof ApiError && e.status === forbiddenStatus) {
        setError("You do not have permission to change bills of material");
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
  const rows = conflict ? diffLines(payloadLines(), conflict.lines ?? []) : [];
  const headerChanges = conflict ? headerDiffs(header, headerOf(conflict)) : [];
  const diffColumns: DataTableColumn<Row>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "yours", header: "Your edit", render: (row) => describe(row.yours) },
    { key: "server", header: "Saved by someone else", render: (row) => describe(row.server) },
    {
      key: "take",
      header: "Merge",
      render: (row) => (
        <Button size="sm" onClick={() => takeSaved(row)}>
          {row.server ? `Use saved line ${row.lineNo}` : `Remove line ${row.lineNo} as saved`}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader title={loaded ? `Edit BOM ${loaded.productId}` : "Edit BOM draft"} description="Change the header and lines of a draft. Active versions change through an ECO." />
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}
        >
          {loadError ? (notFound ? "This BOM version does not exist" : errorText(loadError)) : notDraft ? `Only a draft can be edited; this version is ${loaded?.status}` : error || null}
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
            {rows.length === 0 && headerChanges.length === 0
              ? " Your lines and header now match it."
              : " Each difference below is replaced by your value when you save again, unless you take the saved one."}
          </p>
          {headerChanges.length > 0 ? (
            <div className={styles.toolbar}>
              <p>Header: {headerChanges.join("; ")}</p>
              <Button size="sm" onClick={() => setHeader(headerOf(conflict))}>
                Use saved header
              </Button>
            </div>
          ) : null}
          {rows.length > 0 ? (
            <DataTable label="Lines that differ" columns={diffColumns} rows={rows} getRowKey={(row) => String(row.lineNo)} empty={{ title: "No differences" }} />
          ) : null}
          <div className={styles.toolbar}>
            <Button
              variant="primary"
              onClick={() => {
                setBaseVersion(conflict.version ?? baseVersion);
                setConflict(null);
              }}
            >
              {rows.length === 0 && headerChanges.length === 0 ? `Continue editing version ${conflict.version}` : `Keep my values and continue over version ${conflict.version}`}
            </Button>
            <Button onClick={() => adopt(conflict)}>Discard my edits and load version {conflict.version}</Button>
          </div>
        </Card>
      ) : null}
      {loaded && !notDraft && !conflict ? (
        <Card>
          <form className={styles.form} onSubmit={submit}>
            <div className={styles.field}>
              <FormField id="edit-bom-usage" label="Usage" error={fieldErrors.usage}>
                {(control) => (
                  <Select {...control} value={header.usage} onChange={(e) => setHeader({ ...header, usage: e.target.value })}>
                    {bomUsages.map((u) => (
                      <option key={u} value={u}>
                        {u}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-category" label="Category" error={fieldErrors.category}>
                {(control) => (
                  <Select {...control} value={header.category} onChange={(e) => setHeader({ ...header, category: e.target.value })}>
                    {bomCategories.map((c) => (
                      <option key={c} value={c}>
                        {c}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-site" label="Site" optional error={fieldErrors.siteId}>
                {(control) => <Input {...control} value={header.siteId} onChange={(e) => setHeader({ ...header, siteId: e.target.value })} maxLength={64} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-from" label="Effective from" error={fieldErrors.effectiveFrom}>
                {(control) => <Input {...control} type="date" value={header.effectiveFrom} onChange={(e) => setHeader({ ...header, effectiveFrom: e.target.value })} required />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-to" label="Effective to" optional error={fieldErrors.effectiveTo}>
                {(control) => <Input {...control} type="date" value={header.effectiveTo} onChange={(e) => setHeader({ ...header, effectiveTo: e.target.value })} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-from-qty" label="From quantity" optional error={fieldErrors.fromQty}>
                {(control) => <Input {...control} type="number" min="0" step="any" value={header.fromQty} onChange={(e) => setHeader({ ...header, fromQty: e.target.value })} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="edit-bom-to-qty" label="To quantity" optional error={fieldErrors.toQty}>
                {(control) => <Input {...control} type="number" min="0" step="any" value={header.toQty} onChange={(e) => setHeader({ ...header, toQty: e.target.value })} />}
              </FormField>
            </div>
            {lines.map((line, index) => (
              <Fragment key={line.key}>
                <div className={styles.field}>
                  <FormField id={`edit-bom-component-${line.key}`} label={`Component ${index + 1}`} error={fieldErrors[`lines[${index}]`]}>
                    {(control) => (
                      <Input {...control} value={line.componentProductId} onChange={(e) => updateLine(line.key, { componentProductId: e.target.value })} required maxLength={64} />
                    )}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-bom-qty-${line.key}`} label={`Quantity ${index + 1}`}>
                    {(control) => (
                      <Input {...control} type="number" min="0" step="any" value={line.quantity} onChange={(e) => updateLine(line.key, { quantity: e.target.value })} required />
                    )}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-bom-scrap-${line.key}`} label={`Scrap % ${index + 1}`} optional>
                    {(control) => (
                      <Input {...control} type="number" min="0" max="100" step="any" value={line.scrapVarPct} onChange={(e) => updateLine(line.key, { scrapVarPct: e.target.value })} />
                    )}
                  </FormField>
                </div>
                <div className={styles.field}>
                  <FormField id={`edit-bom-alt-${line.key}`} label={`Alternative group ${index + 1}`} optional>
                    {(control) => <Input {...control} value={line.altGroup} onChange={(e) => updateLine(line.key, { altGroup: e.target.value })} maxLength={2} />}
                  </FormField>
                </div>
                {line.altGroup.trim() ? (
                  <>
                    <div className={styles.field}>
                      <FormField id={`edit-bom-strategy-${line.key}`} label={`Alternative strategy ${index + 1}`}>
                        {(control) => (
                          <Select {...control} value={line.altStrategy} onChange={(e) => updateLine(line.key, { altStrategy: e.target.value })}>
                            <option value="priority">priority</option>
                            <option value="usage_probability">usage probability</option>
                          </Select>
                        )}
                      </FormField>
                    </div>
                    <div className={styles.field}>
                      <FormField id={`edit-bom-altvalue-${line.key}`} label={line.altStrategy === "priority" ? `Priority ${index + 1}` : `Probability % ${index + 1}`}>
                        {(control) => (
                          <Input
                            {...control}
                            type="number"
                            min={line.altStrategy === "priority" ? "1" : "0.0001"}
                            max={line.altStrategy === "priority" ? "99" : "100"}
                            step={line.altStrategy === "priority" ? "1" : "0.0001"}
                            value={line.altValue}
                            onChange={(e) => updateLine(line.key, { altValue: e.target.value })}
                            required
                          />
                        )}
                      </FormField>
                    </div>
                  </>
                ) : null}
                {lines.length > 1 ? (
                  <Button size="sm" onClick={() => setLines((existing) => existing.filter((l) => l.key !== line.key))}>
                    Remove line {index + 1}
                  </Button>
                ) : null}
              </Fragment>
            ))}
            <div className={styles.toolbar}>
              <Button onClick={addLine}>Add line</Button>
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
