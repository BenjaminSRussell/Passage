import { useSearchParams } from "react-router-dom";
import { Card } from "../../shell/ui";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
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

/** Ledger filter: the ledger is picked by name, and the page shows it as soon as one is chosen. */
export function LedgerPicker({ id, label, ledgerId, onChange }: { id: string; label: string; ledgerId: string; onChange: (id: string) => void }) {
  return (
    <Card>
      <form className={styles.form} onSubmit={(event) => event.preventDefault()} noValidate aria-label={label}>
        <LookupFormField id={id} label="Ledger" entity="ledger" value={ledgerId} onChange={onChange} />
      </form>
    </Card>
  );
}
