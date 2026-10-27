import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, queryAsBuilt, type AsBuiltRecord, type Problem } from "./api/client";
import { validTrackingId } from "./AsBuiltPanel";
import { trackingLabel } from "./labels";
import styles from "./pages.module.css";

/** as-built: what went into a finished serial; where-used: which finished serial took a component serial. */
export type Direction = "as-built" | "where-used";

export interface AsBuiltInquiryPageProps {
  /** Opens the production order of a record. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<AsBuiltRecord>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "finishedSerialOrBatch", header: "Finished serial or batch", mono: true },
  { key: "componentItemId", header: "Component", mono: true },
  { key: "tracking", header: "Tracking", render: (r) => trackingLabel[r.tracking] ?? r.tracking },
  { key: "componentSerialOrBatch", header: "Component serial or batch", mono: true },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "assignedAt", header: "Assigned", mono: true, render: (r) => <time dateTime={r.assignedAt}>{r.assignedAt.slice(0, 10)}</time> },
];

/**
 * As-built and where-used inquiry (PRD 4.21.14, FR-21.19, AC-21.14): the components of a finished
 * serial or batch, or the finished serial a component serial went into, across orders.
 */
export function AsBuiltInquiryPage({ onOpenOrder }: AsBuiltInquiryPageProps) {
  const [direction, setDirection] = useState<Direction>("as-built");
  const [serial, setSerial] = useState("");
  const [error, setError] = useState("");
  const [result, setResult] = useState<{
    rows: AsBuiltRecord[];
    loading: boolean;
    failure: Problem | null;
    searched: string;
    direction: Direction;
  } | null>(null);
  const latest = useRef(0);

  const search = async (event: FormEvent) => {
    event.preventDefault();
    if (!validTrackingId(serial)) {
      setError("Enter a serial or batch number (up to 100 characters, no surrounding spaces).");
      return;
    }
    setError("");
    const token = ++latest.current;
    const searched = { searched: serial, direction };
    setResult({ rows: [], loading: true, failure: null, ...searched });
    try {
      const rows = await queryAsBuilt(direction === "as-built" ? { finishedSerialOrBatch: serial } : { componentSerialOrBatch: serial });
      if (token === latest.current) {
        setResult({ rows, loading: false, failure: null, ...searched });
      }
    } catch (err) {
      if (token === latest.current) {
        setResult({ rows: [], loading: false, failure: describeProblem(err), ...searched });
      }
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="As-built and where-used"
        description="Trace the component serials and batches of a finished serial, or the finished serial a component went into."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={search} noValidate aria-label="Trace a serial">
          <FormField id="ab-direction" label="Trace">
            {(control) => (
              <Select {...control} value={direction} onChange={(e) => setDirection(e.target.value as Direction)}>
                <option value="as-built">As-built of a finished serial or batch</option>
                <option value="where-used">Where-used of a component serial or batch</option>
              </Select>
            )}
          </FormField>
          <FormField id="ab-serial" label="Serial or batch number" error={error}>
            {(control) => <Input {...control} value={serial} onChange={(e) => setSerial(e.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary" loading={result?.loading ?? false}>
            Trace
          </Button>
        </form>
      </Card>
      {result?.failure ? (
        <Alert tone="danger" title="Could not trace the serial" live="alert">
          {result.failure.message} ({result.failure.code})
        </Alert>
      ) : null}
      {result && !result.failure ? (
        <DataTable
          label={result.direction === "as-built" ? `As-built of ${result.searched}` : `Where-used of ${result.searched}`}
          columns={columns}
          rows={result.rows}
          loading={result.loading}
          getRowKey={(r) => r.id}
          onRowOpen={onOpenOrder ? (r) => onOpenOrder(r.orderId) : undefined}
          empty={{
            title: `Nothing recorded for ${result.searched}`,
            description:
              result.direction === "as-built"
                ? "No components are assigned to this finished serial or batch."
                : "This component serial or batch is not assigned to a finished serial.",
          }}
        />
      ) : null}
      {!result ? <p className={styles.hint}>Enter a serial or batch number to trace it.</p> : null}
    </div>
  );
}
