import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listExpiringCertifications, type ExpiringCertification, type Problem } from "./api/client";
import { dash, DATE, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The reminder lead times of BR-42.8, widest first. */
const WINDOWS = ["60", "30", "7"];

const columns: DataTableColumn<ExpiringCertification>[] = [
  { key: "personnelNumber", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.personnelNumber}</Link> },
  { key: "legalName", header: "Name" },
  { key: "code", header: "Certificate", mono: true },
  { key: "name", header: "Title" },
  { key: "expiresOn", header: "Expires", mono: true, render: (row) => row.expiresOn ?? dash },
  {
    key: "daysLeft",
    header: "Days left",
    align: "end",
    render: (row) => <Badge tone={row.daysLeft <= 7 ? "danger" : row.daysLeft <= 30 ? "warning" : "neutral"}>{row.daysLeft}</Badge>,
  },
];

/** Certifications expiring (8.42.14, BR-42.8): who must renew what, soonest first. Renewed certificates are left out. */
export function ExpiringCertificationsPage() {
  const [params, setParams] = useSearchParams();
  const withinDays = params.get("withinDays") ?? "60";
  const asOf = params.get("asOf") ?? todayIso();
  const [date, setDate] = useState(asOf);
  const [dateError, setDateError] = useState<string | undefined>();
  const state = useLoad((signal) => listExpiringCertifications({ withinDays, asOf }, signal), `${withinDays}|${asOf}`);
  const [more, setMore] = useState<{ key: string; items: ExpiringCertification[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const key = `${withinDays}|${asOf}`;
  const extra = more && more.key === key ? more : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const cursor = extra ? extra.cursor : state.data?.nextCursor;

  function apply(patch: Record<string, string>) {
    const next = new URLSearchParams(params);
    for (const [name, value] of Object.entries(patch)) next.set(name, value);
    setParams(next);
  }

  function submitDate(event: FormEvent) {
    event.preventDefault();
    if (!DATE.test(date)) {
      setDateError("Enter a date.");
      return;
    }
    setDateError(undefined);
    apply({ asOf: date });
  }

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listExpiringCertifications({ withinDays, asOf, cursor });
      setMore({ key, items: [...(extra?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Expiring certifications"
        description="Certificates valid on the date that expire within the window. A certificate stops being valid on its expiry day, and dispatch stops matching on it."
      />
      <Card>
        <form className={styles.toolbar} onSubmit={submitDate} noValidate aria-label="Expiring certifications filter">
          <FormField id="expiring-window" label="Expiring within">
            {(control) => (
              <Select {...control} value={withinDays} onChange={(event) => apply({ withinDays: event.target.value })}>
                {(WINDOWS.includes(withinDays) ? WINDOWS : [withinDays, ...WINDOWS]).map((d) => (
                  <option key={d} value={d}>
                    {d} days
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="expiring-asOf" label="On" error={dateError}>
            {(control) => <Input {...control} type="date" value={date} invalid={Boolean(dateError)} onChange={(event) => setDate(event.target.value)} />}
          </FormField>
          <Button type="submit">Show</Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load expiring certifications" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Expiring certifications"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "Nothing expires in this window", description: "No valid certificate expires within the chosen number of days." }}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more certificates" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
