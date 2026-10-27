import { useState, type FormEvent } from "react";
import { Alert, Button, Card, EmptyState, FormField, PageHeader, Textarea } from "../../shell/ui";
import { describeProblem, testMaps, type MapTestResponse, type Problem } from "./api/client";
import { IssueList, MessagePill, OrderView } from "./parts";
import styles from "./pages.module.css";

/** Run the 850 map over a sample interchange without storing anything (FR-35.3 test harness). */
export function MapTesterPage() {
  const [raw, setRaw] = useState("");
  const [result, setResult] = useState<MapTestResponse | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (raw.trim() === "") {
      setError("Paste an interchange to test.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      setResult(await testMaps(raw));
    } catch (err) {
      setResult(null);
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="EDI map tester"
        description="Paste a raw X12 interchange to see how each transaction set maps to the canonical model and what the 997 would say. Nothing is stored."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <FormField id="map-raw" label="Raw X12 interchange" hint="ISA through IEA. Only 850 purchase orders are mapped so far." error={error}>
              {(control) => <Textarea {...control} mono rows={10} value={raw} onChange={(event) => setRaw(event.target.value)} invalid={Boolean(error)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run map test
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The interchange could not be read" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      {result ? (
        result.transactions.length === 0 ? (
          <Card>
            <EmptyState compact headingLevel={3} title="No transactions found" description="The interchange has no ST..SE transaction sets." />
          </Card>
        ) : (
          <ul className={styles.results} aria-label="Transactions">
            {result.transactions.map((t, i) => (
              <li key={`${t.stControl}-${i}`}>
                <Card title={`${t.transactionSet} · ST02 ${t.stControl}`} headingLevel={3} actions={<MessagePill status={t.status} />}>
                  <div className={styles.stack}>
                    <IssueList issues={t.issues} label={`Findings for ${t.transactionSet} ${t.stControl}`} />
                    {t.order ? <OrderView order={t.order} label={`Lines of ${t.transactionSet} ${t.stControl}`} /> : null}
                  </div>
                </Card>
              </li>
            ))}
          </ul>
        )
      ) : null}
    </div>
  );
}
