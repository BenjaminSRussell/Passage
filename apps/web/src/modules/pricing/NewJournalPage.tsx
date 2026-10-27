import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { createPriceJournal, describeProblem, newKey, type JournalLineInput, type Problem } from "./api/client";
import { SIGNED_DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";

export interface JournalDraft {
  legalEntityId: string;
  name: string;
  smartRound: boolean;
  lines: string;
}

type Errors = Partial<Record<keyof JournalDraft, string>>;

export const emptyJournal: JournalDraft = { legalEntityId: "", name: "", smartRound: false, lines: "" };

export type ParsedLines = { ok: true; lines: JournalLineInput[] } | { ok: false; error: string };

/**
 * Parses one operation per row (FR-26.9):
 *   UPDATE, <record id>, <version>, <percent>   raise (or lower, with a minus) the stored amount
 *   DELETE, <record id>, <version>              withdraw the record
 * The version is the one shown on the agreement; a record changed since then fails validation.
 */
export function parseJournalLines(text: string): ParsedLines {
  const lines: JournalLineInput[] = [];
  const rows = text.split(/\r?\n/);
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i].trim();
    if (row === "") continue;
    const [op = "", recordId = "", version = "", pct, ...rest] = row.split(",").map((p) => p.trim());
    const where = `Row ${i + 1}`;
    const verb = op.toUpperCase();
    if (verb !== "UPDATE" && verb !== "DELETE") return { ok: false, error: `${where}: start with UPDATE or DELETE.` };
    if (!UUID.test(recordId)) return { ok: false, error: `${where}: the record id must be a UUID.` };
    if (!/^[1-9]\d*$/.test(version)) return { ok: false, error: `${where}: the version must be a whole number from 1.` };
    if (rest.length > 0 || (verb === "DELETE" && pct !== undefined)) return { ok: false, error: `${where}: too many values.` };
    if (verb === "DELETE") {
      lines.push({ op: "DELETE", recordId, expectedVersion: Number(version) });
      continue;
    }
    if (pct === undefined || !SIGNED_DECIMAL.test(pct) || Number(pct) <= -100 || Number(pct) >= 100000 || Number(pct) === 0 || (pct.split(".")[1] ?? "").length > 4) {
      return { ok: false, error: `${where}: give a non-zero percentage above -100 with at most 4 decimals, such as 5 or -2.5.` };
    }
    lines.push({ op: "UPDATE", recordId, expectedVersion: Number(version), adjustPct: pct });
  }
  if (lines.length === 0) return { ok: false, error: "Add at least one line." };
  return { ok: true, lines };
}

export function validateJournal(v: JournalDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  const parsed = parseJournalLines(v.lines);
  if (!parsed.ok) errors.lines = parsed.error;
  return errors;
}

/** Create an OPEN price journal; it changes nothing until it is validated and posted. */
export function NewJournalPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<JournalDraft>(emptyJournal);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<JournalDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJournal(values);
    setErrors(found);
    setFailure(null);
    const parsed = parseJournalLines(values.lines);
    if (Object.keys(found).length > 0 || !parsed.ok) return;
    setBusy(true);
    try {
      const created = await createPriceJournal(
        { legalEntityId: values.legalEntityId.trim(), name: values.name.trim(), smartRound: values.smartRound, lines: parsed.lines },
        key.current,
      );
      navigate(`/pricing/journals/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const line = /^lines\[(\d+)\]/.exec(name);
        if (line) fieldErrors.lines = `Row ${Number(line[1]) + 1}: ${message}`;
        else if (name === "legalEntityId" || name === "name") fieldErrors[name] = message;
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New price journal"
        description="The journal is saved as open. Validate it to see every line's before and after values, then post it in one step."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="pj-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
            {(c) => (
              <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />
            )}
          </FormField>
          <FormField id="pj-name" label="Journal name" error={errors.name}>
            {(c) => <Input {...c} maxLength={120} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField
              id="pj-lines"
              label="Lines"
              hint="One per row: UPDATE, record id, version, percent (for example 5 or -2.5), or DELETE, record id, version."
              error={errors.lines}
            >
              {(c) => <Textarea {...c} mono rows={8} value={values.lines} onChange={(e) => update({ lines: e.target.value })} invalid={Boolean(errors.lines)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="pj-smart" type="checkbox" checked={values.smartRound} onChange={(e) => update({ smartRound: e.target.checked })} />
              <label htmlFor="pj-smart">Smart rounding</label>
            </span>
            <span className={styles.note}>Round adjusted prices up to .99 below 10, .95 below 100 and whole amounts above.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the journal" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save journal
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/journals">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
