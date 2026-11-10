import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, ENVIRONMENTS, receiveX12, type Environment, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/**
 * Record an X12 interchange a partner sent outside AS2/SFTP (for example by
 * email). The service resolves the partner, checks for a duplicate ISA13 and
 * returns the 997; the result opens the interchange.
 */
export function ReceiveInterchangePage() {
  const navigate = useNavigate();
  const [raw, setRaw] = useState("");
  const [environment, setEnvironment] = useState<Environment>("PROD");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!raw.trim().startsWith("ISA")) {
      setError("Paste the whole interchange, starting with the ISA segment.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const result = await receiveX12(raw, environment);
      navigate(`/integration/interchanges/${result.interchangeId}`);
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  const failureTitle =
    failure?.code === "EDI-0102"
      ? "This interchange was already received"
      : failure?.code === "EDI-0101"
        ? "The sender is not a known partner"
        : "Could not receive the interchange";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Receive interchange"
        description="Records the interchange as if the partner had sent it and generates the 997. A control number seen in the last 30 days is refused."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="receive-environment" label="Environment" hint="The partner profile is looked up in this environment.">
            {(control) => (
              <Select {...control} value={environment} onChange={(event) => setEnvironment(event.target.value as Environment)}>
                {ENVIRONMENTS.map((env) => (
                  <option key={env} value={env}>
                    {env}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="receive-raw" label="Raw X12 interchange" hint="ISA through IEA, as the partner sent it." error={error}>
              {(control) => <Textarea {...control} mono rows={12} value={raw} onChange={(event) => setRaw(event.target.value)} invalid={Boolean(error)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failureTitle} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Receive interchange
            </Button>
            <Link className={buttonClassName("ghost")} to="/integration/interchanges">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
