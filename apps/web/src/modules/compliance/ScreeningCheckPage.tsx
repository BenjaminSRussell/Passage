import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, screenSubject, SUBJECT_TYPES, type ListEntry, type Problem, type ScreeningHit, type ScreeningResult, type ScreeningSubject, type SubjectType } from "./api/client";
import { hitStateTone, scoreText, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const COUNTRY = /^[A-Za-z]{2}$/;

/**
 * Parses one list entry per line, fields separated by "|":
 * list code | entry id | version | name | country | address | aliases (separated by ";").
 * Country, address and aliases may be left out; blank lines are skipped.
 */
export function parseEntries(text: string): { entries: ListEntry[]; error?: string } {
  const entries: ListEntry[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [listCode = "", entryId = "", version = "", name = "", country = "", address = "", aliases = ""] = lines[i].split("|").map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!listCode || !entryId) return { entries: [], error: `${at}: start with the list code and the entry id.` };
    const v = Number(version);
    if (!Number.isInteger(v) || v < 1) return { entries: [], error: `${at}: the version must be a whole number from 1.` };
    if (!name) return { entries: [], error: `${at}: give the listed name.` };
    if (country && !COUNTRY.test(country)) return { entries: [], error: `${at}: the country must be a two-letter code such as RU.` };
    const id = `${listCode.toUpperCase()}|${entryId}`;
    if (seen.has(id)) return { entries: [], error: `${at}: entry ${entryId} of ${listCode.toUpperCase()} is listed twice.` };
    seen.add(id);
    const entry: ListEntry = { listCode: listCode.toUpperCase(), entryId, version: v, name };
    if (country) entry.country = country.toUpperCase();
    if (address) entry.address = address;
    const names = aliases
      .split(";")
      .map((a) => a.trim())
      .filter(Boolean);
    if (names.length > 0) entry.aliases = names;
    entries.push(entry);
  }
  return { entries };
}

export interface ScreenValues {
  type: SubjectType;
  id: string;
  name: string;
  address: string;
  country: string;
  entries: string;
}

type Errors = Partial<Record<Exclude<keyof ScreenValues, "type">, string>>;

/** Checks the form and builds the request; mirrors the service's field checks. */
export function toScreenInput(v: ScreenValues): { subject?: ScreeningSubject; entries?: ListEntry[]; errors: Errors } {
  const errors: Errors = {};
  if (!UUID.test(v.id.trim())) errors.id = "Enter the party's or document's UUID.";
  if (!v.name.trim()) errors.name = "Enter the name to screen.";
  if (v.country.trim() && !COUNTRY.test(v.country.trim())) errors.country = "Use a two-letter country code such as DE.";
  const parsed = parseEntries(v.entries);
  if (parsed.error) errors.entries = parsed.error;
  else if (parsed.entries.length === 0) errors.entries = "Paste at least one list entry to screen against.";
  if (Object.keys(errors).length > 0) return { errors };
  const subject: ScreeningSubject = { type: v.type, id: v.id.trim().toLowerCase(), name: v.name.trim() };
  if (v.address.trim()) subject.address = v.address.trim();
  if (v.country.trim()) subject.country = v.country.trim().toUpperCase();
  return { subject, entries: parsed.entries, errors };
}

const columns: DataTableColumn<ScreeningHit>[] = [
  { key: "listCode", header: "List", mono: true },
  { key: "listEntryName", header: "List entry", render: (h) => <Link to={`/compliance/screening/hits/${h.hitId}`}>{h.listEntryName || h.listEntryId}</Link> },
  { key: "score", header: "Score", align: "end", mono: true, render: (h) => scoreText(h.score) },
  { key: "matchedFields", header: "Matched on", render: (h) => h.matchedFields.join(", ") },
  { key: "state", header: "State", render: (h) => <Badge tone={hitStateTone[h.state]}>{sentence(h.state)}</Badge> },
];

const initial: ScreenValues = { type: "PARTY", id: "", name: "", address: "", country: "", entries: "" };

/**
 * Screen a party or document on demand (FR-48.19, duty compliance.trade-screening.view).
 * New hits are stored OPEN for the workbench; the tenant's threshold applies.
 * Until the list store is loaded daily, paste the list entries to check.
 */
export function ScreeningCheckPage() {
  const [values, setValues] = useState<ScreenValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ScreeningResult | null>(null);

  function set<K extends keyof ScreenValues>(name: K, value: ScreenValues[K]) {
    setValues((v) => ({ ...v, [name]: value }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { subject, entries, errors: found } = toScreenInput(values);
    setErrors(found);
    if (!subject || !entries) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await screenSubject(subject, entries));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const summary = result
    ? result.blocked
      ? `Blocked: ${result.hits.length} ${result.hits.length === 1 ? "hit holds" : "hits hold"} this ${values.type === "PARTY" ? "party" : "document"} until an officer decides.`
      : "No hit. Nothing holds this subject."
    : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Screen a party or document"
        description="Check a customer, vendor, bank or worker, or a sales order, delivery, purchase order or payment, against sanctions and denied-party list entries. Matches at or above your tenant's threshold open a hit."
        actions={<Link to="/compliance/screening/hits">Screening hits</Link>}
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Screening check">
          <FormField id="screen-type" label="Subject type">
            {(control) => (
              <Select {...control} value={values.type} onChange={(e) => set("type", e.target.value as SubjectType)}>
                {SUBJECT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="screen-id" label="Subject id" error={errors.id}>
            {(control) => <Input {...control} className={styles.mono} value={values.id} onChange={(e) => set("id", e.target.value)} invalid={Boolean(errors.id)} />}
          </FormField>
          <FormField id="screen-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} onChange={(e) => set("name", e.target.value)} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="screen-country" label="Country" optional error={errors.country}>
            {(control) => <Input {...control} maxLength={2} value={values.country} onChange={(e) => set("country", e.target.value.toUpperCase())} invalid={Boolean(errors.country)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="screen-address" label="Address" optional>
              {(control) => <Input {...control} value={values.address} onChange={(e) => set("address", e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="screen-entries"
              label="List entries"
              hint='One per line: list code | entry id | version | name | country | address | aliases separated by ";". Country, address and aliases may be left out.'
              error={errors.entries}
            >
              {(control) => <Textarea {...control} rows={5} mono value={values.entries} onChange={(e) => set("entries", e.target.value)} invalid={Boolean(errors.entries)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Screen
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The screening check failed" : undefined} live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone={result?.blocked ? "warning" : "success"} live="status">
        {summary}
      </Alert>
      {result ? (
        <DataTable
          label="Blocking hits"
          columns={columns}
          rows={result.hits}
          getRowKey={(h) => h.hitId}
          empty={{ title: "No blocking hits", description: "No list entry matches, or every match was cleared as a false positive for this name, address and country." }}
        />
      ) : (
        <Card>
          <EmptyState icon="info" title="Nothing screened yet" description="Enter the subject and the list entries, then screen." compact />
        </Card>
      )}
    </div>
  );
}
