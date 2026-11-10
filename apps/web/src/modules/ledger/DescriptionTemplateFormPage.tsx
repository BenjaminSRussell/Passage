import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  DESCRIPTION_FIELDS,
  fetchDescriptionTemplate,
  putDescriptionTemplate,
  type DescriptionField,
  type DescriptionTemplate,
  type DescriptionTemplateInput,
} from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** Entry description and line text limit (FR-11.17, LEDGER-0031). */
export const MAX_DESCRIPTION = 255;
const SOURCE_TYPE = /^[a-z][a-z0-9_]{0,59}$/;
const LANGUAGE = /^[a-z]{2}(-[A-Z]{2})?$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const FIELD_LABELS: Record<DescriptionField, string> = {
  voucher: "Voucher",
  party_name: "Party name",
  document_date: "Document date",
  amount_txn: "Amount",
  currency_txn: "Currency",
  source_id: "Source document id",
  period: "Period",
};

/** Values the preview substitutes; postings use the entry's own. */
export const SAMPLE_VALUES: Record<DescriptionField, string> = {
  voucher: "GL-0000123",
  party_name: "Acme Supplies",
  document_date: "2026-03-31",
  amount_txn: "1250.00",
  currency_txn: "USD",
  source_id: "INV-1001",
  period: "2026-03",
};

export interface TemplateDraft {
  scope: DescriptionTemplate["scope"];
  legalEntityId: string;
  sourceType: string;
  language: string;
  template: string;
  /** Placeholder digit to field; entries for placeholders no longer in the text are ignored. */
  bindings: Record<string, string>;
}

type Errors = Record<string, string>;

export const emptyTemplate: TemplateDraft = {
  scope: "tenant",
  legalEntityId: "",
  sourceType: "",
  language: "en",
  template: "",
  bindings: {},
};

/** The distinct placeholder digits of a template, in order: "%2 of %1 %2" gives ["1", "2"]. */
export function placeholdersOf(template: string): string[] {
  return [...new Set(Array.from(template.matchAll(/%([1-9])/g), (m) => m[1]))].sort();
}

/** Client checks that mirror PUT /description-templates/{id} (FR-11.17); the service has the last word. */
export function validateTemplate(v: TemplateDraft): Errors {
  const errors: Errors = {};
  if (!SOURCE_TYPE.test(v.sourceType)) errors.sourceType = "Use lowercase letters, digits and underscores, starting with a letter.";
  if (!LANGUAGE.test(v.language)) errors.language = "Use a language code such as en or en-US.";
  if (v.scope === "legal_entity" && !UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (v.template.trim() === "") errors.template = "Enter the template text.";
  else if ([...v.template].length > MAX_DESCRIPTION) errors.template = `Keep the template to ${MAX_DESCRIPTION} characters.`;
  for (const p of placeholdersOf(v.template)) {
    if (!(DESCRIPTION_FIELDS as readonly string[]).includes(v.bindings[p] ?? "")) {
      errors[`binding-${p}`] = `Pick the field for %${p}.`;
    }
  }
  return errors;
}

/** The request body: only placeholders that appear in the text are bound, as the service requires. */
export function toTemplateInput(v: TemplateDraft): DescriptionTemplateInput {
  const fieldBindings: Record<string, string> = {};
  for (const p of placeholdersOf(v.template)) fieldBindings[p] = v.bindings[p];
  const input: DescriptionTemplateInput = {
    scope: v.scope,
    sourceType: v.sourceType,
    language: v.language,
    template: v.template,
    fieldBindings,
  };
  if (v.scope === "legal_entity") input.legalEntityId = v.legalEntityId.trim();
  return input;
}

/** Expands the template with SAMPLE_VALUES and cuts it like the service does (254 characters and an ellipsis). */
export function previewDescription(template: string, bindings: Record<string, string>): { text: string; truncated: boolean } {
  const text = template.replace(/%([1-9])/g, (_m, p: string) => SAMPLE_VALUES[bindings[p] as DescriptionField] ?? "");
  const chars = [...text];
  if (chars.length <= MAX_DESCRIPTION) return { text, truncated: false };
  return { text: chars.slice(0, MAX_DESCRIPTION - 1).join("") + "…", truncated: true };
}

function TemplateForm({ id, initial, existing, lockVersion }: { id: string; initial: TemplateDraft; existing: boolean; lockVersion?: number }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState<TemplateDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const update = (patch: Partial<TemplateDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const placeholders = placeholdersOf(values.template);
  const preview = previewDescription(values.template, values.bindings);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateTemplate(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putDescriptionTemplate(id, toTemplateInput(values), lockVersion);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "description-templates"] });
      navigate("/ledger/description-templates");
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Edit description template ${initial.sourceType}` : "New description template"}>
        <FormField id="dt-source-type" label="Source type" hint="The posting's source type, such as manual_journal or vendor_invoice." error={errors.sourceType}>
          {(c) => (
            <Input {...c} className={styles.mono} value={values.sourceType} maxLength={60} onChange={(e) => update({ sourceType: e.target.value.toLowerCase() })} invalid={Boolean(errors.sourceType)} />
          )}
        </FormField>
        <FormField id="dt-language" label="Language" hint="Postings use en until journal names set a language." error={errors.language}>
          {(c) => <Input {...c} className={styles.mono} value={values.language} maxLength={5} onChange={(e) => update({ language: e.target.value })} invalid={Boolean(errors.language)} />}
        </FormField>
        <FormField id="dt-scope" label="Applies to">
          {(c) => (
            <Select {...c} value={values.scope} onChange={(e) => update({ scope: e.target.value as TemplateDraft["scope"] })}>
              <option value="tenant">All legal entities</option>
              <option value="legal_entity">One legal entity</option>
            </Select>
          )}
        </FormField>
        {values.scope === "legal_entity" ? (
          <FormField id="dt-legal-entity" label="Legal entity id" hint="Its template wins over the one for all legal entities." error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
        ) : null}
        <div className={styles.wide}>
          <FormField id="dt-template" label="Template" hint="Up to 255 characters. Type %1 to %9 where a field goes, then pick each field below." error={errors.template}>
            {(c) => <Input {...c} value={values.template} onChange={(e) => update({ template: e.target.value })} invalid={Boolean(errors.template)} />}
          </FormField>
        </div>
        <fieldset className={styles.group}>
          <legend className={styles.legend}>Placeholders</legend>
          {placeholders.length === 0 ? (
            <span className={styles.note}>The template has no placeholders, so every posting gets the same text.</span>
          ) : (
            placeholders.map((p) => (
              <FormField key={p} id={`dt-binding-${p}`} label={`%${p}`} error={errors[`binding-${p}`]}>
                {(c) => (
                  <Select {...c} value={values.bindings[p] ?? ""} onChange={(e) => update({ bindings: { ...values.bindings, [p]: e.target.value } })} invalid={Boolean(errors[`binding-${p}`])}>
                    <option value="">Choose a field</option>
                    {DESCRIPTION_FIELDS.map((f) => (
                      <option key={f} value={f}>
                        {FIELD_LABELS[f]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            ))
          )}
        </fieldset>
        <div className={styles.wide}>
          <span className={styles.term}>Preview with sample values</span>
          <p className={styles.value} data-testid="dt-preview">
            {preview.text || "—"}
          </p>
          {preview.truncated ? <p className={styles.note}>Longer than 255 characters: postings will be cut like this and report it.</p> : null}
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the template" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save template
          </Button>
          <Link className={buttonClassName("ghost")} to="/ledger/description-templates">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Create a default description template (FR-11.17). */
export function NewDescriptionTemplatePage() {
  const [id] = useState(() => crypto.randomUUID());
  return (
    <div className={styles.stack}>
      <PageHeader title="New description template" description="Pick the source type it describes and write the text with its placeholders." />
      <TemplateForm id={id} initial={emptyTemplate} existing={false} />
    </div>
  );
}

function toDraft(t: DescriptionTemplate): TemplateDraft {
  return {
    scope: t.scope,
    legalEntityId: t.legalEntityId ?? "",
    sourceType: t.sourceType,
    language: t.language,
    template: t.template,
    bindings: t.fieldBindings,
  };
}

/** Edit an existing description template. */
export function EditDescriptionTemplatePage() {
  const { id = "" } = useParams();
  const result = useQuery({ queryKey: ["ledger", "description-templates", id], queryFn: () => fetchDescriptionTemplate(id) });

  return (
    <div className={styles.stack}>
      <PageHeader title="Description template" description="Changes apply to postings from now on; posted entries keep their text." />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the description template" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the description template…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <TemplateForm id={id} initial={toDraft(result.data)} existing lockVersion={result.data.lockVersion} />
      )}
    </div>
  );
}
