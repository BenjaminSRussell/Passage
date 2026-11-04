import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { CONDITION_TYPES, describeProblem, exportConditionRecords, listConditionRecords, RECORD_STATUSES, type ConditionRecord, type ConditionType, type Problem, type RecordStatus, type RecordsExport } from "./api/client";
import { describeCustomer, describePercent, describeProduct, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ConditionRecord>[] = [
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] },
  { key: "customerKey", header: "Customer", render: describeCustomer },
  { key: "productKey", header: "Product", mono: true, render: describeProduct },
  { key: "currency", header: "Currency", mono: true },
  { key: "fromQuantity", header: "From qty", mono: true, align: "end" },
  { key: "amount", header: "Amount", mono: true, align: "end" },
  { key: "discPct1", header: "Discount", mono: true, render: (row) => describePercent(row) || "—" },
  { key: "validFrom", header: "Validity", mono: true, render: (row) => `${row.validFrom} – ${row.validTo}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

function download(file: RecordsExport) {
  const url = URL.createObjectURL(new Blob([file.csv], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = file.filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/** Trade agreements grid (PRD §5.26.6, §5.26.14): condition records with filters. */
export function AgreementsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RecordStatus | "">("");
  const [type, setType] = useState<ConditionType | "">("");
  const [customer, setCustomer] = useState("");
  const [product, setProduct] = useState("");
  const [asOf, setAsOf] = useState("");
  const [validToFrom, setValidToFrom] = useState("");
  const [validToTo, setValidToTo] = useState("");
  const filter = { status, type, customer: customer.trim(), product: product.trim(), asOf, validToFrom, validToTo };
  const state = useLoad((signal) => listConditionRecords(filter, signal), JSON.stringify(filter));
  const [exporting, setExporting] = useState(false);
  const [exported, setExported] = useState<RecordsExport | null>(null);
  const [exportFailure, setExportFailure] = useState<Problem | null>(null);

  /** Downloads every record that matches the filters, not only the page on screen. */
  async function exportCsv() {
    setExporting(true);
    setExported(null);
    setExportFailure(null);
    try {
      const file = await exportConditionRecords(filter);
      download(file);
      setExported(file);
    } catch (error) {
      setExportFailure(describeProblem(error));
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Trade agreements"
        description="Price, line discount and total discount records with their validity and quantity breaks."
        actions={
          <div className={styles.actions}>
            <Button variant="secondary" loading={exporting} onClick={() => void exportCsv()}>
              Export CSV
            </Button>
            <Link className={buttonClassName("primary")} to="/pricing/agreements/new">
              New agreement
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="ta-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as RecordStatus | "")}>
              <option value="">Any status</option>
              {RECORD_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s.charAt(0) + s.slice(1).toLowerCase()}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ta-type" label="Type">
          {(c) => (
            <Select {...c} value={type} onChange={(e) => setType(e.target.value as ConditionType | "")}>
              <option value="">Any type</option>
              {CONDITION_TYPES.map((t) => (
                <option key={t} value={t}>
                  {TYPE_LABELS[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ta-customer" label="Customer or group" optional>
          {(c) => <Input {...c} value={customer} onChange={(e) => setCustomer(e.target.value)} />}
        </FormField>
        <FormField id="ta-product" label="Product or group" optional>
          {(c) => <Input {...c} value={product} onChange={(e) => setProduct(e.target.value)} />}
        </FormField>
        <FormField id="ta-asof" label="Valid on" optional>
          {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />}
        </FormField>
        <FormField id="ta-ends-from" label="Ends on or after" optional>
          {(c) => <Input {...c} type="date" value={validToFrom} onChange={(e) => setValidToFrom(e.target.value)} />}
        </FormField>
        <FormField id="ta-ends-by" label="Ends on or before" optional>
          {(c) => <Input {...c} type="date" value={validToTo} onChange={(e) => setValidToTo(e.target.value)} />}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {exported ? `Exported ${exported.count} trade agreements to ${exported.filename}.${exported.truncated ? " More records matched than the file holds; narrow the filters." : ""}` : ""}
      </Alert>
      {exportFailure ? (
        <Alert tone="danger" title="Could not export the trade agreements" live="alert">
          {exportFailure.message} ({exportFailure.code})
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load the trade agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Trade agreements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No trade agreements match", description: "Create an agreement, or clear the filters to see every record." }}
          onRowOpen={(row) => navigate(`/pricing/agreements/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 records. Narrow the filters to find others.</p> : null}
    </div>
  );
}
