import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Button, Card, FormField, Input } from "../../shell/ui";
import { UUID } from "./pages";
import styles from "./pages.module.css";

/**
 * The ledger the structure and dimension screens work on, kept in `?ledger=` so
 * the pages are linkable and the form pages can return to the same list.
 */
export function useLedgerParam(): [string, (id: string) => void] {
  const [params, setParams] = useSearchParams();
  const ledgerId = params.get("ledger") ?? "";
  return [ledgerId, (id: string) => setParams(id ? { ledger: id } : {})];
}

/** `?ledger=<id>` for links between the ledger setup pages. */
export function ledgerQuery(ledgerId: string): string {
  return ledgerId ? `?ledger=${encodeURIComponent(ledgerId)}` : "";
}

/** Ledger id filter: checks the UUID before any request is made. */
export function LedgerPicker({ id, label, ledgerId, onChange }: { id: string; label: string; ledgerId: string; onChange: (id: string) => void }) {
  const [draft, setDraft] = useState(ledgerId);
  const [error, setError] = useState<string | undefined>();

  function submit(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!UUID.test(value)) {
      setError("Enter the ledger id as a UUID.");
      return;
    }
    setError(undefined);
    onChange(value);
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={label}>
        <FormField id={id} label="Ledger id" error={error}>
          {(c) => <Input {...c} className={styles.mono} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary">
            Show ledger
          </Button>
        </div>
      </form>
    </Card>
  );
}
