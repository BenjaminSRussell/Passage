import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, ConfirmDialog, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, escalateBlocking, listBlockings, releaseBlocking, type Blocking, type BlockingState, type Problem } from "./api/client";
import { dash, describeDimensions, sentence, shortId } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, RowAction, stateTones } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Scrapping writes stock off; the service also needs it (INV-BLK-008), this only hides the action. */
export const DUTY_BLOCKING_SCRAP = "inventory.blocking.scrap";

/**
 * Blocking and quarantine inquiry (PRD 3.16.14) with the dispositions of FR-16.8: release back to
 * available stock, move a quarantine to blocked, or scrap (write the stock off, after confirmation).
 */
export function BlockingsPage() {
  const [state, setState] = useState<BlockingState | "">("ACTIVE");
  const [attempt, setAttempt] = useState(0);
  const load = useLoad((signal) => listBlockings(state, signal), `${state}|${attempt}`);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [scrapping, setScrapping] = useState<Blocking | null>(null);
  const [scrapBusy, setScrapBusy] = useState(false);
  const canScrap = holdsDuty(usePrincipalDuties(), DUTY_BLOCKING_SCRAP);

  async function scrap(row: Blocking) {
    setScrapBusy(true);
    try {
      await releaseBlocking(row.id, row.version, "SCRAP");
      setFailure(null);
      setNotice(`Scrapped ${row.qty} ${row.unit}: the stock was written off.`);
      setAttempt((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setScrapBusy(false);
      setScrapping(null);
    }
  }

  const columns: DataTableColumn<Blocking>[] = [
    { key: "physicalDate", header: "Blocked on", mono: true },
    { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
    { key: "qty", header: "Quantity", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
    { key: "targetStatus", header: "Status", render: (row) => <Badge tone={stateTones[row.targetStatus] ?? "neutral"}>{sentence(row.targetStatus)}</Badge> },
    { key: "reason", header: "Reason", render: (row) => (row.note ? `${sentence(row.reason)}: ${row.note}` : sentence(row.reason)) },
    { key: "expectedReleaseDate", header: "Expected release", mono: true, render: (row) => row.expectedReleaseDate ?? dash },
    {
      key: "state",
      header: "State",
      render: (row) =>
        row.state === "RELEASED" ? (
          <>
            {`Released${row.disposition ? ` (${sentence(row.disposition)})` : ""}`}
            {row.scrapJournalId ? (
              <>
                {" "}
                <Link to={`/inventory/journals/${row.scrapJournalId}`}>Journal {shortId(row.scrapJournalId)}</Link>
              </>
            ) : null}
          </>
        ) : (
          "Active"
        ),
    },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.state === "ACTIVE" ? (
          <div className={styles.rowActions}>
            <RowAction
              label="Release"
              name={`blocking ${shortId(row.id)}`}
              run={() => releaseBlocking(row.id, row.version)}
              onDone={() => {
                setFailure(null);
                setNotice(`Released ${row.qty} ${row.unit} back to available stock.`);
                setAttempt((n) => n + 1);
              }}
              onError={setFailure}
            />
            {row.targetStatus === "QUARANTINE" ? (
              <RowAction
                label="Move to blocked"
                name={`blocking ${shortId(row.id)}`}
                run={() => escalateBlocking(row.id, row.version)}
                onDone={() => {
                  setFailure(null);
                  setNotice(`Moved ${row.qty} ${row.unit} from quarantine to blocked.`);
                  setAttempt((n) => n + 1);
                }}
                onError={setFailure}
              />
            ) : null}
            {canScrap ? (
              <Button size="sm" onClick={() => setScrapping(row)} aria-label={`Scrap blocking ${shortId(row.id)}`}>
                Scrap
              </Button>
            ) : null}
          </div>
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Blocking and quarantine"
        description="Stock held out of availability for quality, damage, recall or legal reasons. It stays in physical stock and keeps its value."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/blockings/new">
            Block stock
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="blockings-state" label="State">
          {(control) => (
            <Select {...control} value={state} onChange={(event) => setState(event.target.value as BlockingState | "")}>
              <option value="ACTIVE">Active</option>
              <option value="RELEASED">Released</option>
              <option value="">All</option>
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {load.error ? <ProblemAlert title="Could not load blockings" problem={load.error} onRetry={load.reload} /> : null}
      {scrapping ? (
        <ConfirmDialog
          title="Scrap this blocked stock?"
          description={`${scrapping.qty} ${scrapping.unit} will be written off at its current value through a posted adjustment journal. This cannot be undone; a mistaken write-off is corrected by reversing that journal.`}
          confirmLabel={scrapBusy ? "Scrapping" : "Scrap stock"}
          cancelLabel="Keep stock"
          onCancel={() => (scrapBusy ? undefined : setScrapping(null))}
          onConfirm={() => void scrap(scrapping)}
        />
      ) : null}
      {failure ? <ProblemAlert title="The blocking was not changed" problem={failure} /> : null}
      {load.error ? null : (
        <DataTable
          label="Blockings"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No blocked stock", description: "Block or quarantine a quantity, batch or serial to keep it out of availability." }}
        />
      )}
    </div>
  );
}
