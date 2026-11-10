import { useMemo, useState, type ChangeEvent, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Textarea,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  actOnCatalogImport,
  decideImportRows,
  describeProblem,
  getCatalogImport,
  listCatalogImports,
  listImportRows,
  newIdempotencyKey,
  stageVendorImport,
  type CatalogImport,
  type CatalogImportAction,
  type CatalogImportStatus,
  type ImportDecisions,
  type ImportRow,
  type ImportRowStatus,
} from "./api/client";
import {
  Details,
  LoadError,
  ShortId,
  StatusBadge,
  statusLabel,
  useResource,
  uuidPattern,
  type Failure,
} from "./parts";
import styles from "./pages.module.css";

const IMPORT_STATUSES: CatalogImportStatus[] = [
  "STAGED",
  "COMMITTED",
  "CANCELLED",
];
const ROW_STATUSES: ImportRowStatus[] = [
  "ACCEPTABLE",
  "ERROR",
  "ACCEPTED",
  "REJECTED",
];

const listColumns: DataTableColumn<CatalogImport>[] = [
  {
    key: "createdAt",
    header: "Staged",
    mono: true,
    render: (i) => i.createdAt.slice(0, 10),
  },
  {
    key: "fileName",
    header: "File",
    render: (i) => i.fileName || "Pasted rows",
  },
  {
    key: "status",
    header: "Status",
    render: (i) => <StatusBadge status={i.status} />,
  },
  { key: "totalRows", header: "Rows", align: "end" },
  { key: "acceptableRows", header: "To review", align: "end" },
  { key: "errorRows", header: "Errors", align: "end" },
  {
    key: "vendorId",
    header: "Vendor",
    render: (i) => <ShortId id={i.vendorId} />,
  },
];

/** Staged vendor catalog files with a status filter and load-more paging (FR-17.18). */
export function CatalogImportsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CatalogImportStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<CatalogImport[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listCatalogImports({ status }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor catalog imports"
        description="Vendor price files wait here until a buyer accepts their rows and adds them to a catalog."
        actions={
          <Link
            className={buttonClassName("secondary")}
            to="/purchasing/catalogs"
          >
            Catalogs
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="import-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as CatalogImportStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {IMPORT_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {page.error ? (
        <LoadError
          what="vendor imports"
          error={page.error}
          onRetry={page.reload}
        />
      ) : null}
      <DataTable
        label="Vendor catalog imports"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(i) => i.id}
        loading={page.loading && rows.length === 0}
        empty={{
          title: "No vendor imports yet",
          description:
            "Open a catalog and choose Import vendor file to stage a price list.",
        }}
        onRowOpen={(i) => navigate(`/purchasing/catalog-imports/${i.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button
            loading={page.loading}
            onClick={() => setCursor(page.data?.nextCursor)}
          >
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Larger files are refused before upload; the service takes a 32 MiB request (FR-20.19 caps uploads at 25 MB). */
const MAX_FILE_BYTES = 25 * 1024 * 1024;

const CSV_HINT =
  "Vendor part number,Name,Category,Unit,Unit price,Currency,Lead time days\nG-100,Safety gloves,MRO,PR,4.25,USD,3";

/** Stage a vendor CSV against a catalog: choose a file or paste the rows (FR-17.18). */
export function CatalogImportCreatePage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [vendorId, setVendorId] = useState("");
  const [fileName, setFileName] = useState("");
  const [csv, setCsv] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [vendorId, fileName, csv]);

  async function onFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_FILE_BYTES) {
      setErrors({ csv: "The file is larger than 25 MB. Split it into smaller files of at most 50,000 rows." });
      return;
    }
    setErrors({});
    setFileName(file.name);
    setCsv(await file.text());
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!uuidPattern.test(vendorId.trim()))
      found.vendorId = "Enter the vendor id (a UUID).";
    if (csv.trim() === "") found.csv = "Choose a CSV file or paste its rows.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await stageVendorImport(
        id,
        {
          vendorId: vendorId.trim(),
          format: "CSV",
          fileName: fileName || undefined,
          csv,
        },
        key,
      );
      navigate(`/purchasing/catalog-imports/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import vendor file"
        description="Rows are checked and staged. Nothing reaches the catalog until you accept rows and commit."
        actions={
          <Link
            className={buttonClassName("secondary")}
            to={`/purchasing/catalogs/${id}`}
          >
            Back to catalog
          </Link>
        }
      />
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Vendor file">
          <div className={styles.grid}>
            <FormField
              id="import-vendor"
              label="Vendor"
              error={errors.vendorId}
            >
              {(c) => (
                <EntityLookup entity="vendor"
                  {...c}
                  value={vendorId}
                  onChange={(e) => setVendorId(e.target.value)}
                />
              )}
            </FormField>
            <FormField
              id="import-file"
              label="CSV file"
              optional
              hint="Or paste the rows below"
            >
              {(c) => (
                <Input
                  {...c}
                  type="file"
                  accept=".csv,text/csv"
                  onChange={(e) => void onFile(e)}
                />
              )}
            </FormField>
          </div>
          <FormField
            id="import-csv"
            label="Rows"
            error={errors.csv}
            hint="First line is the header. Name, category, unit, price and currency columns are required; at most 50,000 rows."
          >
            {(c) => (
              <Textarea
                {...c}
                mono
                rows={8}
                placeholder={CSV_HINT}
                value={csv}
                onChange={(e) => setCsv(e.target.value)}
              />
            )}
          </FormField>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Stage file
            </Button>
          </div>
        </Card>
      </form>
    </div>
  );
}

const rowColumns = (
  onDecide: (row: ImportRow, decision: "accept" | "reject") => void,
  busy: string | null,
  canDecide: boolean,
): DataTableColumn<ImportRow>[] => [
  { key: "rowNo", header: "Row", align: "end", mono: true },
  {
    key: "status",
    header: "Status",
    render: (r) => <StatusBadge status={r.status} />,
  },
  {
    key: "partNo",
    header: "Part number",
    mono: true,
    render: (r) => r.raw.vendorPartNo ?? "",
  },
  { key: "name", header: "Item", render: (r) => r.raw.name ?? "" },
  { key: "category", header: "Category", render: (r) => r.raw.category ?? "" },
  { key: "unit", header: "Unit", render: (r) => r.raw.unit ?? "" },
  {
    key: "price",
    header: "Price",
    align: "end",
    mono: true,
    render: (r) =>
      r.raw.unitPrice
        ? `${r.raw.unitPrice} ${r.raw.currency ?? ""}`.trim()
        : "",
  },
  { key: "reason", header: "Problem", render: (r) => r.reason ?? "" },
  ...(canDecide
    ? [
        {
          key: "decide",
          header: "Decision",
          render: (r: ImportRow) =>
            r.status === "ERROR" ? null : (
              <>
                {r.status !== "ACCEPTED" ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={busy === `accept-${r.rowNo}`}
                    onClick={() => onDecide(r, "accept")}
                  >
                    Accept row {r.rowNo}
                  </Button>
                ) : null}
                {r.status !== "REJECTED" ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={busy === `reject-${r.rowNo}`}
                    onClick={() => onDecide(r, "reject")}
                  >
                    Reject row {r.rowNo}
                  </Button>
                ) : null}
              </>
            ),
        },
      ]
    : []),
];

/** One staged import: counts, rows with accept and reject, commit and cancel (FR-17.18, AC-17.13). */
export function CatalogImportDetailPage() {
  const { id = "" } = useParams();
  const header = useResource((signal) => getCatalogImport(id, signal), [id]);
  const [changed, setChanged] = useState<CatalogImport | null>(null);
  const [status, setStatus] = useState<ImportRowStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<ImportRow[]>([]);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const imp = changed ?? header.data?.body ?? null;
  const version = imp?.version;
  const actionKey = useMemo(() => newIdempotencyKey(), [version]);
  const page = useResource(
    async (signal) => {
      const result = await listImportRows(
        id,
        { status },
        { cursor, signal, limit: 100 },
      );
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [id, status, cursor, version],
  );

  async function run(
    label: string,
    call: (i: CatalogImport) => Promise<{ body: CatalogImport }>,
    done: (i: CatalogImport) => string,
  ) {
    if (!imp) return;
    setBusy(label);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await call(imp);
      setCursor(undefined);
      setChanged(body);
      setNotice(done(body));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const decide = (label: string, decisions: ImportDecisions, done: string) =>
    run(
      label,
      (i) =>
        decideImportRows(i.id, decisions, i.version, `${actionKey}-${label}`),
      () => done,
    );
  const act = (action: CatalogImportAction) =>
    run(
      action,
      (i) =>
        actOnCatalogImport(i.id, action, i.version, `${actionKey}-${action}`),
      (i) =>
        action === "commit"
          ? `Added ${i.entriesAdded} items to the catalog.`
          : "The import was cancelled.",
    );

  if (header.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Vendor catalog import" />
        {header.error.code === "PRC-CAT-IMPORT-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Import not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link
                  className={buttonClassName("secondary")}
                  to="/purchasing/catalog-imports"
                >
                  Back to imports
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError
            what="the import"
            error={header.error}
            onRetry={header.reload}
          />
        )}
      </div>
    );
  }
  if (!imp) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Vendor catalog import" />
        <p className={styles.muted} role="status">
          Loading import…
        </p>
      </div>
    );
  }

  const staged = imp.status === "STAGED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor catalog import"
        description={imp.fileName || "Pasted rows"}
        meta={<StatusBadge status={imp.status} />}
        actions={
          staged ? (
            <>
              <Button
                variant="secondary"
                loading={busy === "cancel"}
                disabled={busy !== null && busy !== "cancel"}
                onClick={() => void act("cancel")}
              >
                Cancel import
              </Button>
              <Button
                variant="primary"
                loading={busy === "commit"}
                disabled={
                  (busy !== null && busy !== "commit") || imp.acceptedRows === 0
                }
                onClick={() => void act("commit")}
              >
                Add {imp.acceptedRows} accepted to catalog
              </Button>
            </>
          ) : (
            <Link
              className={buttonClassName("secondary")}
              to={`/purchasing/catalogs/${imp.catalogId}`}
            >
              Open catalog
            </Link>
          )
        }
      />
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Summary">
        <Details
          items={[
            { label: "Rows in file", value: String(imp.totalRows), mono: true },
            {
              label: "To review",
              value: String(imp.acceptableRows),
              mono: true,
            },
            { label: "Accepted", value: String(imp.acceptedRows), mono: true },
            { label: "Rejected", value: String(imp.rejectedRows), mono: true },
            { label: "With errors", value: String(imp.errorRows), mono: true },
            {
              label: "Added to catalog",
              value: String(imp.entriesAdded),
              mono: true,
            },
            { label: "Vendor", value: imp.vendorId, mono: true },
            { label: "Catalog", value: imp.catalogId, mono: true },
          ]}
        />
      </Card>
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="import-row-status" label="Row status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as ImportRowStatus | "");
                }}
              >
                <option value="">All rows</option>
                {ROW_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        {staged ? (
          <Button
            loading={busy === "accept-all"}
            disabled={
              (busy !== null && busy !== "accept-all") ||
              imp.acceptableRows === 0
            }
            onClick={() =>
              void decide(
                "accept-all",
                { acceptAll: true },
                "Every row without errors is accepted.",
              )
            }
          >
            Accept all {imp.acceptableRows} rows without errors
          </Button>
        ) : null}
      </div>
      {page.error ? (
        <LoadError what="the rows" error={page.error} onRetry={page.reload} />
      ) : null}
      <DataTable
        label="Staged rows"
        columns={rowColumns(
          (r, d) =>
            void decide(
              `${d}-${r.rowNo}`,
              d === "accept" ? { accept: [r.rowNo] } : { reject: [r.rowNo] },
              d === "accept"
                ? `Row ${r.rowNo} accepted.`
                : `Row ${r.rowNo} rejected.`,
            ),
          busy,
          staged,
        )}
        rows={page.error ? [] : rows}
        getRowKey={(r) => String(r.rowNo)}
        loading={page.loading && rows.length === 0}
        empty={{
          title: "No rows",
          description: status
            ? "No row has this status."
            : "The file had no rows.",
        }}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button
            loading={page.loading}
            onClick={() => setCursor(page.data?.nextCursor)}
          >
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
