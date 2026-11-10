import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { createJournal, listJournals, type IntrastatJournal } from "./api/client";
import { FailureAlert, LoadAlert, monthLabel, taxKey, useAction } from "./shared";

export const flowLabel = { arrival: "Arrivals (purchases)", dispatch: "Dispatches (sales)" } as const;

export function journalTitle(j: Pick<IntrastatJournal, "flow" | "period_from" | "correction_of">): string {
  return `${j.flow === "arrival" ? "Arrivals" : "Dispatches"}, ${monthLabel(j.period_from)}${j.correction_of ? " (correction)" : ""}`;
}

export function IntrastatJournalsPage() {
  const navigate = useNavigate();
  const list = useQuery({ queryKey: taxKey("intrastat-journals"), queryFn: () => listJournals() });
  const [flow, setFlow] = useState<"arrival" | "dispatch">("dispatch");
  const [month, setMonth] = useState(() => new Date().toISOString().slice(0, 7));
  const [error, setError] = useState("");
  const create = useAction(({ f, m }: { f: "arrival" | "dispatch"; m: string }) => createJournal(f, `${m}-01`), [taxKey("intrastat-journals")], (j) => navigate(`/tax/intrastat/journals/${j.id}`));

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!/^\d{4}-\d{2}$/.test(month)) {
      setError("Choose the month of the declaration.");
      return;
    }
    setError("");
    create.run({ f: flow, m: month });
  }

  const columns: DataTableColumn<IntrastatJournal>[] = [
    { key: "title", header: "Declaration", render: (j) => <Link to={`/tax/intrastat/journals/${j.id}`}>{journalTitle(j)}</Link> },
    { key: "status", header: "Status", render: (j) => <StatusPill status={j.status} /> },
    { key: "line_count", header: "Lines", align: "end" },
    { key: "error_count", header: "Lines with problems", align: "end" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intrastat declarations"
        description="One journal per month and direction. Add the shipments or receipts, check them, merge matching lines, then export the declaration."
        actions={<Link to="/tax/intrastat/master">Master data</Link>}
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New Intrastat journal">
          <FormField id="journal-flow" label="Direction">
            {(c) => (
              <Select {...c} value={flow} onChange={(e) => setFlow(e.target.value as "arrival" | "dispatch")}>
                {Object.entries(flowLabel).map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="journal-month" label="Month" error={error}>
            {(c) => <Input {...c} type="month" value={month} onChange={(e) => setMonth(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <div className={styles.formActions}>
            <FailureAlert title="Could not open the journal" failure={create.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={create.pending}>
              Open journal
            </Button>
          </div>
        </form>
      </Card>
      {list.isError ? (
        <LoadAlert title="Could not load the Intrastat journals" error={list.error} onRetry={() => void list.refetch()} />
      ) : (
        <DataTable label="Intrastat journals" columns={columns} rows={list.data ?? []} getRowKey={(j) => j.id} loading={list.isPending} empty={{ title: "No Intrastat journals yet", description: "Open a journal for the month you need to declare." }} />
      )}
    </div>
  );
}
