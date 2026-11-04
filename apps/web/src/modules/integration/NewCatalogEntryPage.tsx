import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { CATALOG_KINDS, DUTY_HUB_CATALOG_WRITE, newKey, publishCatalogEntry, type CatalogKind, type NewCatalogEntry } from "./api/hubClient";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";

export interface CatalogValues {
  kind: CatalogKind;
  name: string;
  specVersion: string;
  title: string;
  summary: string;
  ownerService: string;
  /** The OpenAPI or AsyncAPI document as JSON text. */
  spec: string;
}

type Errors = Partial<Record<keyof CatalogValues, string>>;

export const EMPTY_CATALOG: CatalogValues = { kind: "api", name: "", specVersion: "", title: "", summary: "", ownerService: "", spec: "" };

const NAME = /^[a-z0-9][a-z0-9._-]{1,99}$/;
const OWNER = /^[a-z0-9][a-z0-9._-]{1,63}$/;
export const SEMVER = /^\d{1,6}\.\d{1,6}\.\d{1,6}$/;
const MAX_SPEC_BYTES = 2 * 1024 * 1024;

/** Checks that mirror the hub (src/catalog.ts), and the request the form sends. */
export function validateCatalogEntry(v: CatalogValues): { errors: Errors; input?: NewCatalogEntry } {
  const errors: Errors = {};
  const name = v.name.trim();
  if (!NAME.test(name)) errors.name = "Use 2 to 100 characters: a-z, 0-9, dot, underscore or hyphen, starting with a letter or digit.";
  const specVersion = v.specVersion.trim();
  if (!SEMVER.test(specVersion)) errors.specVersion = "Enter MAJOR.MINOR.PATCH, such as 1.4.0.";
  const title = v.title.trim();
  if (title === "" || title.length > 200) errors.title = "Enter a title of at most 200 characters.";
  if (v.summary.length > 2000) errors.summary = "Use at most 2000 characters.";
  const owner = v.ownerService.trim();
  if (!OWNER.test(owner)) errors.ownerService = "Enter the owning service, such as sales or integration-hub.";
  let spec: Record<string, unknown> | null = null;
  if (v.spec.trim() === "") errors.spec = "Paste the document or choose a JSON file.";
  else if (v.spec.length > MAX_SPEC_BYTES) errors.spec = "The document is larger than 2 MiB.";
  else {
    try {
      const parsed: unknown = JSON.parse(v.spec);
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) errors.spec = "The document must be a JSON object.";
      else spec = parsed as Record<string, unknown>;
    } catch {
      errors.spec = "The document is not valid JSON. Convert YAML to JSON first.";
    }
  }
  if (spec !== null && errors.specVersion === undefined) {
    const info = spec["info"];
    const declared = typeof info === "object" && info !== null ? (info as Record<string, unknown>)["version"] : undefined;
    if (declared !== specVersion) errors.specVersion = `Use the document's info.version (${String(declared)}): the two must be the same.`;
  }
  if (Object.keys(errors).length > 0 || spec === null) return { errors };
  const input: NewCatalogEntry = { kind: v.kind, name, specVersion, title, ownerService: owner, spec };
  if (v.summary.trim() !== "") input.summary = v.summary.trim();
  return { errors, input };
}

/** Publish one version of an OpenAPI or AsyncAPI document to the catalog (FR-35.8). */
export function NewCatalogEntryPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CATALOG_WRITE);
  const [values, setValues] = useState<CatalogValues>(EMPTY_CATALOG);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CatalogValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (file) update({ spec: await file.text() });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCatalogEntry(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await publishCatalogEntry(input, key.current);
      navigate(`/integration/catalog/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HUB-0409") setErrors({ specVersion: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Exclude<keyof CatalogValues, "kind" | "spec" | "summary">, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`catalog-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<CatalogValues>)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  if (!canWrite) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Publish contract" />
        <Alert tone="warning" title="You cannot publish to the catalog">
          Publishing needs the duty {DUTY_HUB_CATALOG_WRITE}.
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Publish contract"
        description="Add one version of an OpenAPI 3 or AsyncAPI 3 document. A published version never changes; the next version of a major version must be higher."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="catalog-kind" label="Kind">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(event) => update({ kind: event.target.value as CatalogKind })}>
                {CATALOG_KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("name", "Contract name", "Versions of one contract share a name, such as sales-api.", { spellCheck: false, maxLength: 100 })}
          {text("specVersion", "Version", "MAJOR.MINOR.PATCH, the document's info.version, higher than the newest published version of its major version.", { spellCheck: false, maxLength: 20 })}
          {text("title", "Title", undefined, { maxLength: 200 })}
          {text("ownerService", "Owner service", "The service that owns the contract, such as sales.", { spellCheck: false, maxLength: 64 })}
          <FormField id="catalog-summary" label="Summary" optional error={errors.summary}>
            {(control) => <Textarea {...control} rows={2} value={values.summary} maxLength={2000} onChange={(event) => update({ summary: event.target.value })} invalid={Boolean(errors.summary)} />}
          </FormField>
          <FormField id="catalog-file" label="Load from file" optional hint="A JSON file fills the document box below.">
            {(control) => <input {...control} type="file" accept="application/json,.json" onChange={chooseFile} />}
          </FormField>
          <FormField id="catalog-spec" label="Document (JSON)" error={errors.spec}>
            {(control) => <Textarea {...control} rows={10} mono value={values.spec} onChange={(event) => update({ spec: event.target.value })} invalid={Boolean(errors.spec)} spellCheck={false} />}
          </FormField>
          {failure ? (
            <div className={styles.wide}>
              <ProblemAlert problem={failure} title="Could not publish the contract" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" disabled={busy} loading={busy}>
              Publish
            </Button>
            <Link className={buttonClassName("secondary")} to="/integration/catalog">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
