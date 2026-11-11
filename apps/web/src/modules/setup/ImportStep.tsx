import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, FormField, Select } from "../../shell/ui";
import { csvCell, downloadCsv } from "../../shell/ui/csv";
import { getSpec, listBindings, listConnections, listSpecsPage, previewBinding, type EntityBinding, type MappingSpec } from "../migration/api/client";
import { describeSource } from "../migration/bindingDomain";
import { PreviewPanel } from "../migration/BindingPreview";
import { useLoad } from "../migration/useLoad";
import styles from "./pages.module.css";

/** The first master data a new company imports, by the entity name the migration service knows it under. */
export const importKinds = [
  { entity: "vendor", label: "Vendors", noun: "vendors" },
  { entity: "customer", label: "Customers", noun: "customers" },
  { entity: "product", label: "Products", noun: "products" },
] as const;

export const IMPORT_READ_DUTY = "migration.run";
export const IMPORT_CHECK_DUTY = "migration.connection.write";

/** The source columns a mapping reads, in the order it names them: the header row of the file to import. */
export function templateColumns(mapping: unknown): string[] {
  const fields = (mapping as { fields?: unknown } | null | undefined)?.fields;
  if (!Array.isArray(fields)) return [];
  const columns: string[] = [];
  const add = (name: unknown) => {
    if (typeof name === "string" && name !== "" && !columns.includes(name)) columns.push(name);
  };
  for (const field of fields as { source?: unknown; sources?: unknown; timeSource?: unknown }[]) {
    add(field?.source);
    if (Array.isArray(field?.sources)) field.sources.forEach(add);
    add(field?.timeSource);
  }
  return columns;
}

interface ImportSetup {
  /** Active bindings of the chosen entity that read from a file connection. */
  bindings: EntityBinding[];
  specs: MappingSpec[];
}

async function loadSetup(entity: string, signal: AbortSignal): Promise<ImportSetup> {
  const [bindings, connections, specs] = await Promise.all([
    listBindings({ entity, status: "active", limit: 100 }, signal),
    listConnections({ kind: "file", status: "active", limit: 100 }, signal),
    listSpecsPage({ entity }, signal),
  ]);
  const files = new Set(connections.items.map((c) => c.id));
  return { bindings: bindings.items.filter((b) => files.has(b.connectionProfileId)), specs: specs.items };
}

function TemplateButton({ specId, noun }: { specId: string; noun: string }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const [empty, setEmpty] = useState(false);

  async function download() {
    setBusy(true);
    setFailure(null);
    setEmpty(false);
    try {
      const columns = templateColumns((await getSpec(specId)).mapping);
      if (columns.length === 0) setEmpty(true);
      // No byte order mark: the header row is read back by the import as it is written here.
      else downloadCsv(`${noun}-template.csv`, `${columns.map(csvCell).join(",")}\r\n`);
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Button loading={busy} onClick={() => void download()}>
        Download the CSV template
      </Button>
      {empty ? <p role="status">The mapping names no columns, so there is no template to download.</p> : null}
      {failure ? (
        <Alert tone="danger" live="alert">
          <ProblemMessage problem={failure} fallback="The template could not be downloaded." />
        </Alert>
      ) : null}
    </>
  );
}

/**
 * The import step of the setup guide: check a file of vendors, customers or products before loading it. It uses
 * the migration screens' own preview, so the file is read and validated by the migration service with the
 * tenant's mapping, and each row comes back with what is wrong with it. Nothing is loaded here; loading is a
 * migration run.
 */
export function ImportStep() {
  const duties = usePrincipalDuties();
  const canRead = holdsDuty(duties, IMPORT_READ_DUTY);
  const canCheck = holdsDuty(duties, IMPORT_CHECK_DUTY);
  const [entity, setEntity] = useState<string>(importKinds[0].entity);
  const [picked, setPicked] = useState("");
  const kind = importKinds.find((k) => k.entity === entity) ?? importKinds[0];
  const setup = useLoad((signal) => (canRead ? loadSetup(entity, signal) : Promise.resolve<ImportSetup>({ bindings: [], specs: [] })), `${entity}:${canRead}`);

  if (!canRead) {
    return (
      <Alert tone="info" title="Your account can't import files">
        Importing needs the duty to run migrations. Ask an administrator to add it to your role, or skip this step and add records by hand.
      </Alert>
    );
  }

  const bindings = setup.data?.bindings ?? [];
  const binding = bindings.find((b) => b.id === picked) ?? bindings[0];
  const specId = binding?.mappingSpecId;

  return (
    <div className={styles.stack}>
      <FormField id="import-kind" label="What to import">
        {(control) => (
          <Select
            {...control}
            value={entity}
            onChange={(event) => {
              setEntity(event.target.value);
              setPicked("");
            }}
          >
            {importKinds.map((k) => (
              <option key={k.entity} value={k.entity}>
                {k.label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {setup.loading ? (
        <p role="status" aria-busy="true">
          Looking for the {kind.noun} import…
        </p>
      ) : null}
      {setup.error ? (
        <Alert tone="danger" live="alert" title="Could not load the import setup" action={<Button onClick={setup.reload}>Try again</Button>}>
          <ProblemMessage problem={setup.error} />
        </Alert>
      ) : null}
      {!setup.loading && !setup.error && binding === undefined ? (
        <Alert tone="info" title={`No file import is set up for ${kind.noun} yet`}>
          <p>An administrator sets this up once. It takes a file connection, a mapping that says which column goes where, and a binding that ties the two together for {kind.noun}.</p>
          <ol className={styles.help}>
            <li>
              <Link to="/migration/connections/new">Create a file connection</Link>
            </li>
            <li>
              <Link to="/migration/specs/new">Publish a mapping for {kind.noun}</Link>
            </li>
            <li>
              <Link to="/migration/bindings/new">Create a binding for {kind.noun}</Link>
            </li>
          </ol>
        </Alert>
      ) : null}
      {!setup.loading && !setup.error && binding !== undefined ? (
        <>
          {bindings.length > 1 ? (
            <FormField id="import-binding" label="Import to use" hint="Each one reads its own kind of file.">
              {(control) => (
                <Select {...control} value={binding.id} onChange={(event) => setPicked(event.target.value)}>
                  {bindings.map((b) => (
                    <option key={b.id} value={b.id}>
                      {describeSource("file", b) || b.keyFields.join(", ")}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          <ol className={styles.help}>
            <li>Download the template and fill in one row for each of your {kind.noun}. Keep the header row as it is.</li>
            <li>Choose your file under Try a file, then select Preview rows. Each row shows the record it becomes, or what to fix.</li>
            <li>When no row is rejected, start the import. Rows that are rejected then are listed on the run, and you can load them again after fixing them.</li>
          </ol>
          <div className={styles.actions}>
            {specId ? <TemplateButton specId={specId} noun={kind.noun} /> : null}
            <Link to="/migration/runs/new" className={buttonClassName()}>
              Start the import
            </Link>
            <Link to="/migration" className={buttonClassName("ghost")}>
              See earlier imports
            </Link>
          </div>
          {specId ? null : <p>This import has no mapping yet, so the preview shows the rows as they are in the file. Choose a mapping in the preview to check them.</p>}
          {canCheck ? (
            <PreviewPanel key={binding.id} isFile specs={setup.data?.specs ?? []} defaultSpecId={specId} run={(options) => previewBinding(binding.id, options)} />
          ) : (
            <Alert tone="info" title="Your account can't check files here">
              Checking a file needs the duty to maintain migration connections. Ask an administrator to add it to your role, or start the import and review the rejected rows on the run.
            </Alert>
          )}
        </>
      ) : null}
    </div>
  );
}
