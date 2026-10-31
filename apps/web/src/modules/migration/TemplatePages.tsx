import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getTemplate, listTemplates, newKey, publishTemplate, type DataTemplate, type Problem } from "./api/client";
import { formatDateTime, parseTemplateForm } from "./domain";
import styles from "./pages.module.css";
import { DUTY_SPEC_WRITE } from "./SpecPages";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<DataTemplate>[] = [
  { key: "entity", header: "Entity", mono: true },
  { key: "name", header: "Name" },
  { key: "version", header: "Version", align: "end" },
  { key: "defaultCount", header: "Defaults", align: "end" },
  { key: "createdAt", header: "Published", render: (row) => formatDateTime(row.createdAt) },
];

const defaultColumns: DataTableColumn<{ target: string; value: string }>[] = [
  { key: "target", header: "Mapping target", mono: true },
  { key: "value", header: "Default value", mono: true },
];

/** Published data templates: default values that fill fields missing from the source before mapping. */
export function TemplatesPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_SPEC_WRITE);
  const [entity, setEntity] = useState("");
  const state = useLoad((signal) => listTemplates(entity.trim() || undefined, signal), entity.trim());
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Data templates"
        description="Project defaults for fields the legacy source leaves empty. A run that names a template fills those fields before the mapping rules and records which ones it filled."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/migration">
              Runs
            </Link>
            {canWrite ? (
              <Link className={buttonClassName("primary")} to="/migration/templates/new">
                Publish template
              </Link>
            ) : null}
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="templates-entity" label="Entity">
          {(control) => <Input {...control} value={entity} onChange={(event) => setEntity(event.target.value)} placeholder="customer" />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load data templates" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Data templates"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No data templates yet", description: "Publish a template to give a migration run project defaults." }}
          onRowOpen={(row) => navigate(`/migration/templates/${row.id}`)}
        />
      )}
    </div>
  );
}

/** Publish a template as an immutable version; the service checks it against the mapping when a run uses it. */
export function NewTemplatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({ name: "", entity: "", version: "1", defaults: "" });
  const [errors, setErrors] = useState<{ name?: string; entity?: string; version?: string; defaults?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function change(field: keyof typeof form, value: string) {
    setForm((prev) => ({ ...prev, [field]: value }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseTemplateForm(form);
    setErrors(parsed.errors);
    if (!parsed.value) return;
    setBusy(true);
    try {
      const saved = await publishTemplate(parsed.value, key.current);
      navigate(`/migration/templates/${saved.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Publish data template" description="Defaults apply only where the source has no value. Publishing the same content again returns the stored version; changed content needs a higher version." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="template-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={form.name} invalid={Boolean(errors.name)} onChange={(event) => change("name", event.target.value)} />}
          </FormField>
          <FormField id="template-entity" label="Entity" error={errors.entity}>
            {(control) => <Input {...control} value={form.entity} invalid={Boolean(errors.entity)} placeholder="customer" onChange={(event) => change("entity", event.target.value)} />}
          </FormField>
          <FormField id="template-version" label="Version" error={errors.version}>
            {(control) => <Input {...control} inputMode="numeric" value={form.version} invalid={Boolean(errors.version)} onChange={(event) => change("version", event.target.value)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="template-defaults" label="Defaults (JSON)" error={errors.defaults} hint="Mapping target to default value, as strings.">
              {(control) => <Textarea {...control} mono rows={8} value={form.defaults} invalid={Boolean(errors.defaults)} onChange={(event) => change("defaults", event.target.value)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not publish the template" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Publish template
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/templates">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One published template with its defaults. */
export function TemplateDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getTemplate(id, signal), id);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Data template" />
        <Alert tone="danger" title="Could not load this template" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/templates">
          Back to templates
        </Link>
      </div>
    );
  }
  const template = state.data?.id === id ? state.data : null;
  if (!template) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Data template" description="Loading..." />
      </div>
    );
  }
  const rows = Object.entries(template.defaults ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([target, value]) => ({ target, value }));
  return (
    <div className={styles.stack}>
      <PageHeader title={`${template.name} v${template.version}`} description={`For ${template.entity}. Published ${formatDateTime(template.createdAt)}. ${template.defaultCount} defaults.`} />
      <Card title="Defaults">
        <DataTable label="Template defaults" columns={defaultColumns} rows={rows} getRowKey={(r) => r.target} empty={{ title: "No defaults", description: "This template has no default values." }} />
      </Card>
      <Card title="Checksum">
        <code>{template.checksum}</code>
      </Card>
      <Link className={buttonClassName("ghost")} to="/migration/templates">
        Back to templates
      </Link>
    </div>
  );
}
