import { useSearchParams } from "react-router-dom";
import { Alert, Card, DataTable, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { oshaExtract, type OshaLine } from "./api/safety";
import { UUID, dayOf } from "./format";
import styles from "./pages.module.css";
import { humanize, LoadFailure, LookupField, TextField } from "./parts";
import { useLoad } from "./useLoad";

const logColumns: DataTableColumn<OshaLine>[] = [
  { key: "caseNo", header: "Case", mono: true, align: "end" },
  { key: "employeeName", header: "Employee" },
  { key: "occurredOn", header: "Date", mono: true, render: (row) => dayOf(row.occurredOn) },
  { key: "location", header: "Where" },
  { key: "incidentType", header: "Type", render: (row) => humanize(row.incidentType) },
  { key: "bodyParts", header: "Body parts", render: (row) => (row.bodyParts?.length ? row.bodyParts.map(humanize).join(", ") : "") },
  { key: "outcome", header: "Outcome", render: (row) => humanize(row.outcome) },
  { key: "daysAway", header: "Days away", mono: true, align: "end" },
  { key: "daysRestricted", header: "Days restricted", mono: true, align: "end" },
];

/** OSHA 300 log, 300A summary and 301 detail for a calendar year. Privacy cases keep the name withheld. */
export function OshaPage() {
  const [params, setParams] = useSearchParams();
  const thisYear = String(new Date().getFullYear());
  const rawYear = params.get("year") ?? thisYear;
  const year = /^\d{4}$/.test(rawYear) ? rawYear : thisYear;
  const yearError = rawYear === year ? undefined : "Enter a four-digit year.";
  const rawSite = params.get("siteId") ?? "";
  const siteId = UUID.test(rawSite) ? rawSite : "";
  const siteError = rawSite !== "" && siteId === "" ? "Enter a site id, or clear the field." : undefined;
  const state = useLoad((signal) => oshaExtract({ year, siteId }, signal), `osha|${year}|${siteId}`);

  const setParam = (name: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value === "") next.delete(name);
    else next.set(name, value);
    setParams(next, { replace: true });
  };

  const extract = state.data;
  const summary = extract?.summary;
  const detailed = extract?.log.filter((line) => line.description) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader title="OSHA logs" description="The 300 log of recordable cases, the 300A annual summary and 301 detail." />
      <div className={styles.toolbar}>
        <TextField id="osha-year" label="Year" inputMode="numeric" maxLength={4} value={rawYear} error={yearError} onChange={(v) => setParam("year", v)} />
        <LookupField entity="site" prefill={false} id="osha-site" label="Site" optional value={rawSite} error={siteError} onChange={(v) => setParam("siteId", v)} />
      </div>
      {state.error ? (
        <LoadFailure error={state.error} reload={state.reload} what="OSHA extract" />
      ) : !extract || !summary ? (
        <div aria-busy="true">
          <Alert tone="info" live="status">
            Loading the OSHA logs
          </Alert>
        </div>
      ) : (
        <>
          {extract.truncated ? (
            <Alert tone="warning" title="The log was cut short">
              The service returned only part of the year. Narrow it with a site.
            </Alert>
          ) : null}
          <Card title={`300A summary for ${extract.year}`}>
            <StatGroup label="300A totals">
              <StatTile label="Deaths" value={String(summary.deaths)} />
              <StatTile label="Days-away cases" value={String(summary.daysAwayCases)} />
              <StatTile label="Restricted cases" value={String(summary.restrictedCases)} />
              <StatTile label="Other recordable cases" value={String(summary.otherRecordableCases)} />
              <StatTile label="Total days away" value={String(summary.totalDaysAway)} />
              <StatTile label="Total days restricted" value={String(summary.totalDaysRestricted)} />
            </StatGroup>
            <p className={styles.note}>
              {Object.entries(summary.byType)
                .map(([type, count]) => `${humanize(type)}: ${count}`)
                .join(", ") || "No cases by type."}
            </p>
          </Card>
          <Card title="300 log">
            <DataTable
              label="OSHA 300 log"
              columns={logColumns}
              rows={extract.log}
              getRowKey={(row) => row.incidentId}
              empty={{ title: "No recordable cases", description: `Nothing was recorded for ${extract.year}.` }}
            />
          </Card>
          {detailed.length > 0 ? (
            <Card title="301 detail">
              <div className={styles.stack}>
                {detailed.map((line) => (
                  <details key={line.incidentId}>
                    <summary>{`Case ${line.caseNo}: ${line.employeeName}`}</summary>
                    <p className={styles.value}>{line.description}</p>
                    {line.witnesses?.length ? <p className={styles.note}>{`Witnesses: ${line.witnesses.join(", ")}`}</p> : null}
                    {line.treatments?.length ? <p className={styles.note}>{`Treatments: ${line.treatments.join(", ")}`}</p> : null}
                  </details>
                ))}
              </div>
            </Card>
          ) : null}
        </>
      )}
    </div>
  );
}
