import { useState } from "react";
import { Link } from "react-router-dom";
import { buttonClassName, DataTable, EmptyState, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchWorklist, type CustomerRisk, type WorklistResponse } from "./api/client";
import { DAY, money, shortId, today, UUID } from "./format";
import { LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<CustomerRisk>[] = [
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "riskScore", header: "Risk score", mono: true, align: "end" },
  { key: "overdue", header: "Overdue", mono: true, align: "end", render: (row) => money(row.overdue, row.currency) },
  { key: "totalOpen", header: "Total open", mono: true, align: "end", render: (row) => money(row.totalOpen, row.currency) },
  { key: "oldestDays", header: "Oldest (days)", mono: true, align: "end" },
  { key: "dunningLevel", header: "Dunning level", mono: true, align: "end" },
  { key: "openDisputes", header: "Open disputes", mono: true, align: "end" },
  { key: "brokenPromises", header: "Broken promises", mono: true, align: "end" },
];

/** Collections worklist (FR-30.6, FR-30.10): each customer's exposure per currency, riskiest first. */
export function WorklistPage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [asOf, setAsOf] = useState(today());
  const entity = legalEntityId.trim();
  const entityValid = UUID.test(entity);
  const dateValid = DAY.test(asOf);
  const ready = entityValid && dateValid;
  const state = useLoad<WorklistResponse | null>(
    (signal) => (ready ? fetchWorklist(entity, asOf, signal) : Promise.resolve(null)),
    ready ? `${entity}|${asOf}` : "",
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Collections worklist"
        description="Customers ranked by risk score: overdue share, days late, dunning level, open disputes and broken promises."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/receivables/disputes">
              Disputes
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/dunning-runs">
              Dunning runs
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField
          id="wl-entity"
          label="Legal entity id"
          error={entity !== "" && !entityValid ? "Enter the legal entity id (a UUID)." : undefined}
        >
          {(c) => (
            <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={entity !== "" && !entityValid} />
          )}
        </FormField>
        <FormField id="wl-as-of" label="As of" error={dateValid ? undefined : "Enter a date as YYYY-MM-DD."}>
          {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={!dateValid} />}
        </FormField>
      </div>
      {!ready ? (
        <EmptyState title="Choose a legal entity" description="The worklist ranks the customers of one legal entity. Enter its id to load it." />
      ) : state.error ? (
        <LoadFailure title="Could not load the worklist" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Collections worklist"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => `${row.customerId}|${row.currency}`}
          loading={state.loading}
          empty={{ title: "Nothing to collect", description: `No customer of this legal entity has an open item as of ${asOf}.` }}
        />
      )}
      <p className={styles.note}>
        The score runs from 0 to 100: overdue share of the open balance 40, open-amount weighted days late 20, dunning level 20, open
        disputes 15, broken promises 5.
      </p>
    </div>
  );
}
