import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listXref, xrefSummary, type Problem, type XrefEntry, type XrefFilter } from "./api/client";
import { formatDateTime } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 50;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const columns: DataTableColumn<XrefEntry>[] = [
  { key: "sourceSystem", header: "Source system", mono: true },
  { key: "entity", header: "Entity", mono: true },
  { key: "sourceKey", header: "Legacy key", mono: true },
  { key: "targetId", header: "Passage id", mono: true },
  { key: "mergedIntoTargetId", header: "Merged into", mono: true, render: (row) => row.mergedIntoTargetId ?? "" },
  { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt) },
];

/** Legacy key cross-reference: where did AX or SAP record X land in Passage, and which legacy keys feed a Passage record. */
export function XrefPage() {
  const [form, setForm] = useState({ sourceSystem: "", entity: "", keyPrefix: "", targetId: "" });
  const targetId = form.targetId.trim();
  // A partly typed or pasted id is not sent: the service would refuse it, so the table keeps the last valid result.
  const targetIdError = targetId && !UUID.test(targetId) ? "Enter a full Passage id (UUID)." : undefined;
  const [filter, setFilter] = useState<XrefFilter>({ limit: PAGE });
  useEffect(() => {
    if (targetIdError) return;
    setFilter({
      sourceSystem: form.sourceSystem || undefined,
      entity: form.entity.trim() || undefined,
      keyPrefix: form.keyPrefix.trim() || undefined,
      targetId: targetId || undefined,
      limit: PAGE,
    });
  }, [form.sourceSystem, form.entity, form.keyPrefix, targetId, targetIdError]);
  const key = JSON.stringify(filter);
  const keyRef = useRef(key);
  keyRef.current = key;
  const summary = useLoad((signal) => xrefSummary(signal), "summary");
  const first = useLoad((signal) => listXref(filter, signal), key);
  const [more, setMore] = useState<XrefEntry[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  // A new first page (new filter or reload) replaces everything fetched after it.
  useEffect(() => {
    setMore([]);
    setCursor(first.data?.nextCursor);
    setMoreError(null);
  }, [first.data]);

  async function loadMore() {
    if (!cursor) return;
    const requested = key;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listXref({ ...filter, cursor });
      // The filter changed while this page was in flight: its rows belong to the old result, so drop them.
      if (keyRef.current !== requested) return;
      setMore((prev) => [...prev, ...page.items]);
      setCursor(page.nextCursor);
    } catch (e) {
      if (keyRef.current === requested) setMoreError(describeProblem(e));
    } finally {
      setLoadingMore(false);
    }
  }

  function change(field: keyof typeof form, value: string) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  const rows = [...(first.data?.items ?? []), ...more];
  const counts = summary.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Legacy key cross-reference"
        description="Every legacy record the migration has mapped, with the Passage id it became. Search by legacy key prefix, or paste a Passage id to see which legacy keys feed it."
        actions={
          <Link className={buttonClassName("ghost")} to="/migration">
            Runs
          </Link>
        }
      />
      {counts.length > 0 ? (
        <StatGroup label="Mapped keys per source system and entity">
          {counts.map((c) => (
            <StatTile key={`${c.sourceSystem}/${c.entity}`} label={`${c.sourceSystem} ${c.entity}`} value={c.count} />
          ))}
        </StatGroup>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="xref-system" label="Source system">
          {(control) => (
            <Select {...control} value={form.sourceSystem} onChange={(event) => change("sourceSystem", event.target.value)}>
              <option value="">All</option>
              <option value="ax2012">AX 2012</option>
              <option value="d365fo">D365 F&amp;O</option>
              <option value="sap_ecc">SAP ECC</option>
              <option value="sap_s4">SAP S/4</option>
            </Select>
          )}
        </FormField>
        <FormField id="xref-entity" label="Entity">
          {(control) => <Input {...control} value={form.entity} onChange={(event) => change("entity", event.target.value)} placeholder="customer" />}
        </FormField>
        <FormField id="xref-prefix" label="Legacy key starts with">
          {(control) => <Input {...control} value={form.keyPrefix} onChange={(event) => change("keyPrefix", event.target.value)} />}
        </FormField>
        <FormField id="xref-target" label="Passage id" error={targetIdError}>
          {(control) => <Input {...control} value={form.targetId} onChange={(event) => change("targetId", event.target.value)} />}
        </FormField>
      </div>
      {first.error ? (
        <Alert tone="danger" title="Could not load the cross-reference" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          {first.error.message} ({first.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Legacy key cross-reference"
          columns={columns}
          rows={rows}
          getRowKey={(row) => `${row.sourceSystem}/${row.entity}/${row.sourceKey}`}
          loading={first.loading}
          empty={{ title: "No mapped keys", description: "Run a migration, or widen the filters, to see legacy keys here." }}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more rows" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor && !first.error ? (
        <div>
          <Button onClick={loadMore} disabled={loadingMore}>
            {loadingMore ? "Loading" : "Load more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
