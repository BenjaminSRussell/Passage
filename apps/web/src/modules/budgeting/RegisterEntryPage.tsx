import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeError, postRegisterEntry, type EntryType, type RegisterLine } from "./api/client";
import { fromUnits, sumAmounts, validateEntryLines } from "./logic";
import styles from "./pages.module.css";

const ENTRY_TYPES: { value: EntryType; label: string }[] = [
  { value: "Original", label: "Original budget" },
  { value: "Revision", label: "Revision" },
  { value: "Transfer", label: "Transfer (nets to zero)" },
  { value: "CarryForward", label: "Carry-forward" },
];

const emptyLine = (): RegisterLine & { key: string } => ({ key: crypto.randomUUID(), dimensionKey: "", spanKey: "", amount: "" });

/** Posts a budget register entry to a rule version (FR-38.1). Posted entries are immutable (BR-38.2). */
export function RegisterEntryPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [entryType, setEntryType] = useState<EntryType>("Original");
  const [lines, setLines] = useState([emptyLine()]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  function update(index: number, patch: Partial<RegisterLine>) {
    setLines((current) => current.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = lines.map(({ dimensionKey, spanKey, amount }) => ({ dimensionKey: dimensionKey.trim(), spanKey: spanKey.trim(), amount: amount.trim() }));
    const found = validateEntryLines(entryType, clean);
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const input = { ruleVersionId: id, entryType, lines: clean };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      await postRegisterEntry(input, key.current.value);
      navigate(`/budgeting/rules/${id}`);
    } catch (err) {
      setFailure(describeError(err, "The entry could not be posted."));
    } finally {
      setBusy(false);
    }
  }

  const net = fromUnits(sumAmounts(lines.map((l) => l.amount.trim())));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Post register entry"
        description="Each line moves the budget of one control combination in one time span. The entry posts to the budget ledger, not the general ledger."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="entry-type" label="Entry type">
            {(c) => (
              <Select {...c} value={entryType} onChange={(e) => setEntryType(e.target.value as EntryType)}>
                {ENTRY_TYPES.map((t) => (
                  <option key={t.value} value={t.value}>
                    {t.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {lines.map((line, i) => (
            <fieldset key={line.key} className={styles.lineSet}>
              <legend className={styles.legend}>Line {i + 1}</legend>
              <FormField id={`entry-dim-${i}`} label="Dimension">
                {(c) => <Input {...c} className={styles.mono} value={line.dimensionKey} onChange={(e) => update(i, { dimensionKey: e.target.value })} />}
              </FormField>
              <FormField id={`entry-span-${i}`} label="Time span" hint="For example FY2026 or 2026-09.">
                {(c) => <Input {...c} className={styles.mono} value={line.spanKey} onChange={(e) => update(i, { spanKey: e.target.value })} />}
              </FormField>
              <FormField id={`entry-amount-${i}`} label="Amount">
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={line.amount} onChange={(e) => update(i, { amount: e.target.value })} />}
              </FormField>
              {lines.length > 1 ? (
                <Button variant="ghost" onClick={() => setLines((current) => current.filter((_, k) => k !== i))}>
                  Remove line {i + 1}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <p className={styles.note}>Net of all lines: {net}</p>
          <div className={styles.formActions}>
            <Button onClick={() => setLines((current) => [...current, emptyLine()])}>Add line</Button>
            <Button type="submit" variant="primary" loading={busy}>
              Post entry
            </Button>
            <Link className={buttonClassName("ghost")} to={`/budgeting/rules/${id}`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {errors.length > 0 ? (
          <ul className={styles.errorList}>
            {errors.map((msg) => (
              <li key={msg}>{msg}</li>
            ))}
          </ul>
        ) : (
          failure
        )}
      </Alert>
    </div>
  );
}
