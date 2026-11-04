import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Button, FormField, Input, PageHeader } from "../../shell/ui";
import { isUuid } from "./format";
import styles from "./pages.module.css";

/** Look up a credit-limit change by id, then open the approval view. */
export function PendingChangesPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [draft, setDraft] = useState(params.get("id") ?? "");
  const [error, setError] = useState("");

  function open(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!isUuid(value)) {
      setError("Enter the pending change id (a UUID).");
      return;
    }
    setError("");
    navigate(`/party/pending-changes/${value}`);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit limit approvals"
        description="Open a held credit-limit increase by its pending-change id. The stored limit does not change until the request is approved."
      />
      <form className={styles.toolbar} onSubmit={open} noValidate aria-label="Open a credit limit change">
        <FormField id="change-id" label="Pending change id" hint="The id returned when an increase was requested." error={error || undefined}>
          {(control) => (
            <Input
              {...control}
              value={draft}
              className={styles.mono}
              maxLength={36}
              onChange={(event) => setDraft(event.target.value)}
              invalid={Boolean(error)}
            />
          )}
        </FormField>
        <Button type="submit" variant="primary">
          Open change
        </Button>
      </form>
      <p className={styles.note}>
        There is no list of open requests on the party API yet, so this page opens one change at a time.
      </p>
    </div>
  );
}
