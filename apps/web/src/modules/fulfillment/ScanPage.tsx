import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { Gs1ParseError, parseGs1, type Gs1Scan } from "./gs1";
import styles from "./pages.module.css";

/**
 * GS1-128 scan panel for the basic pick flow (FR-28.11): the scan field keeps
 * focus, Enter parses, and the fields a pick needs are shown. Rendered without a
 * router, so it uses no links.
 */
export function ScanPage() {
  const [raw, setRaw] = useState("");
  const [scan, setScan] = useState<Gs1Scan | null>(null);
  const [error, setError] = useState<string | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    try {
      setScan(parseGs1(raw.trim()));
      setError(null);
    } catch (e) {
      setScan(null);
      setError(e instanceof Gs1ParseError ? `${e.message} (${e.code})` : "Scan could not be parsed");
    }
  }

  const fields: [string, string | number | undefined][] = scan
    ? [
        ["GTIN", scan.gtin],
        ["SSCC", scan.sscc],
        ["Batch", scan.batch],
        ["Serial", scan.serial],
        ["Expiry (YYMMDD)", scan.expiry],
        ["Quantity", scan.qty],
      ]
    : [];

  return (
    <div className={styles.stack}>
      <PageHeader title="Scan a label" description="Scan or type a GS1-128 barcode, such as (01)00614141000012(10)LOT7(37)5, and press Enter." />
      <Card>
        <form className={styles.toolbar} onSubmit={submit} noValidate>
          <FormField id="gs1-scan" label="GS1-128 scan" hint="Bracketed or raw with FNC1 separators.">
            {(control) => <Input {...control} autoFocus autoComplete="off" value={raw} onChange={(event) => setRaw(event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary">
            Parse scan
          </Button>
        </form>
      </Card>
      {error ? (
        <Alert tone="danger" title="Scan not recognised" live="alert">
          {error}
        </Alert>
      ) : null}
      {scan ? (
        <Card title="Scanned fields">
          <dl className={styles.scanResult} aria-label="Scanned fields">
            {fields
              .filter(([, value]) => value !== undefined)
              .map(([term, value]) => (
                <div key={term} className={styles.entry}>
                  <dt className={styles.term}>{term}</dt>
                  <dd className={`${styles.value} ${styles.mono}`}>{String(value)}</dd>
                </div>
              ))}
          </dl>
        </Card>
      ) : null}
    </div>
  );
}
