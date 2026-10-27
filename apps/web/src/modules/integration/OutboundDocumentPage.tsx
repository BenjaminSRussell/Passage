import { useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, EmptyState, FormField, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import {
  createOutbound,
  describeProblem,
  listPartners,
  newKey,
  OUTBOUND_SETS,
  type OutboundResult,
  type OutboundSet,
  type Problem,
} from "./api/client";
import { OUTBOUND_EXAMPLES, OUTBOUND_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Values {
  partnerId: string;
  transactionSet: OutboundSet;
  document: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Client checks before the request: a partner is chosen and the document is a JSON object. */
export function validateOutbound(values: Values): { errors: Errors; document?: unknown } {
  const errors: Errors = {};
  if (values.partnerId === "") errors.partnerId = "Choose the partner to send the document to.";
  let document: unknown;
  try {
    document = JSON.parse(values.document);
    if (document === null || typeof document !== "object" || Array.isArray(document)) {
      errors.document = "The document must be a JSON object.";
    }
  } catch (error) {
    errors.document = `The document is not valid JSON: ${error instanceof Error ? error.message : "parse error"}.`;
  }
  return Object.keys(errors).length > 0 ? { errors } : { errors, document };
}

/**
 * Generate an outbound 856, 810 or 855 from its canonical document (FR-35.4).
 * The service checks the mapping rules, envelopes it with the partner's next
 * control number and records it; the partner's 997 later marks it acknowledged.
 */
export function OutboundDocumentPage() {
  const [params] = useSearchParams();
  const partners = useLoad((signal) => listPartners({}, signal), "outbound-partners");
  const [values, setValues] = useState<Values>({
    partnerId: params.get("partnerId") ?? "",
    transactionSet: "856",
    document: OUTBOUND_EXAMPLES["856"],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<OutboundResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues((prev) => {
      const next = { ...prev, ...patch };
      // Switching the set swaps in its example unless the document was edited.
      if (patch.transactionSet && prev.document === OUTBOUND_EXAMPLES[prev.transactionSet]) {
        next.document = OUTBOUND_EXAMPLES[patch.transactionSet];
      }
      return next;
    });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, document } = validateOutbound(values);
    setErrors(found);
    setFailure(null);
    if (document === undefined) return;
    setBusy(true);
    try {
      setResult(await createOutbound(values.partnerId, values.transactionSet, document, key.current));
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setResult(null);
      if (problem.code === "EDI-0007") setErrors({ document: problem.message });
      else if (problem.fields.transactionSet) setErrors({ transactionSet: problem.fields.transactionSet });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const header = (
    <PageHeader
      title="Send EDI document"
      description="Build an 856 ship notice, 810 invoice or 855 acknowledgment from its canonical document. The service checks the mapping rules, assigns the partner's next control number and records the interchange until the partner's 997 arrives."
      actions={
        <Link className={buttonClassName("ghost")} to="/integration/interchanges?direction=OUT">
          Outbound interchanges
        </Link>
      }
    />
  );

  if (partners.error) {
    return (
      <div className={styles.stack}>
        {header}
        <Alert tone="danger" title="Could not load partners" live="alert" action={<Button size="sm" onClick={partners.reload}>Try again</Button>}>
          {partners.error.message} ({partners.error.code})
        </Alert>
      </div>
    );
  }
  if (!partners.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <span className="visually-hidden">Loading partners…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const active = partners.data.filter((p) => p.status === "active");
  if (active.length === 0) {
    return (
      <div className={styles.stack}>
        {header}
        <Card>
          <EmptyState
            title="No active trading partners"
            description="Create a partner profile first; documents are addressed to its ISA identity."
            action={
              <Link className={buttonClassName("primary")} to="/integration/partners/new">
                New trading partner
              </Link>
            }
          />
        </Card>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      {header}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="outbound-partner" label="Partner" error={errors.partnerId}>
            {(control) => (
              <Select {...control} value={values.partnerId} onChange={(event) => update({ partnerId: event.target.value })} invalid={Boolean(errors.partnerId)}>
                <option value="">Choose a partner</option>
                {active.map((p) => (
                  <option key={p.id} value={p.id}>
                    {`${p.name} (${p.isaQualifier}/${p.isaId}, ${p.environment})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="outbound-set" label="Document" error={errors.transactionSet}>
            {(control) => (
              <Select
                {...control}
                value={values.transactionSet}
                onChange={(event) => update({ transactionSet: event.target.value as OutboundSet })}
                invalid={Boolean(errors.transactionSet)}
              >
                {OUTBOUND_SETS.map((set) => (
                  <option key={set} value={set}>
                    {OUTBOUND_LABELS[set]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField
              id="outbound-document"
              label="Canonical document (JSON)"
              hint="Prefilled with a valid example. Dates are CCYYMMDD, amounts are decimal strings, GLNs have 13 digits and SSCCs 18 with a GS1 check digit."
              error={errors.document}
            >
              {(control) => (
                <Textarea {...control} mono rows={16} value={values.document} onChange={(event) => update({ document: event.target.value })} invalid={Boolean(errors.document)} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not send the document" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Generate document
            </Button>
            <Button type="button" variant="ghost" onClick={() => update({ document: OUTBOUND_EXAMPLES[values.transactionSet] })}>
              Reset to example
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <Card
          title={`${OUTBOUND_LABELS[result.transactionSet]} recorded as interchange ${result.controlNumber}`}
          headingLevel={2}
          actions={
            <Link className={buttonClassName("secondary")} to={`/integration/interchanges/${result.interchangeId}`}>
              Open interchange
            </Link>
          }
        >
          <div className={styles.stack}>
            <Alert tone="success" title="Document generated" live="status">
              It waits for the partner's 997. Transmission over AS2 or SFTP is not available yet, so send the text below through your current channel.
            </Alert>
            <pre className={styles.payload} aria-label="Generated X12 interchange">
              {result.x12}
            </pre>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
