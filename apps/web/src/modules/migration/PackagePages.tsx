import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  applyPackage,
  describeProblem,
  diffPackage,
  getPackage,
  listPackages,
  newKey,
  type ConfigPackage,
  type ManifestFile,
  type PackageDiff,
  type Problem,
} from "./api/client";
import { formatDateTime, parsePackageInput } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

// Enforced by the service; not seeded in policies/authz/data.json yet (handoff for #313).
export const DUTY_PACKAGE_APPLY = "migration.package.apply";

const columns: DataTableColumn<ConfigPackage>[] = [
  { key: "version", header: "Version", mono: true },
  { key: "schemaVersion", header: "Schema", align: "end" },
  { key: "fileCount", header: "Files", align: "end" },
  { key: "applied", header: "Times applied", align: "end", render: (row) => row.applied.length },
  { key: "last", header: "Last applied", render: (row) => (row.applied.length > 0 ? formatDateTime(row.applied[row.applied.length - 1].appliedAt) : "Never") },
  { key: "createdAt", header: "Recorded", render: (row) => formatDateTime(row.createdAt) },
];

/** Configuration data packages applied to this tenant. */
export function PackagesPage() {
  const navigate = useNavigate();
  const canApply = holdsDuty(usePrincipalDuties(), DUTY_PACKAGE_APPLY);
  const state = useLoad((signal) => listPackages(signal), "packages");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Configuration packages"
        description="Signed setup data applied with a diff first. Applying creates and updates records; it never deletes anything in the target."
        actions={
          canApply ? (
            <Link className={buttonClassName("primary")} to="/migration/packages/new">
              Apply package
            </Link>
          ) : null
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load configuration packages" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Configuration packages"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No configuration packages yet", description: "Preview and apply a signed package to see it here." }}
          onRowOpen={(row) => navigate(`/migration/packages/${row.id}`)}
        />
      )}
    </div>
  );
}

const diffColumns: DataTableColumn<PackageDiff["files"][number]>[] = [
  { key: "name", header: "File", mono: true },
  { key: "entity", header: "Entity", mono: true },
  { key: "create", header: "Create", align: "end", render: (row) => row.create.length },
  { key: "update", header: "Update", align: "end", render: (row) => row.update.length },
  { key: "unchanged", header: "Unchanged", align: "end", render: (row) => row.unchanged.length },
];

function ProblemAlert({ title, problem }: { title: string; problem: Problem }) {
  return (
    <Alert tone="danger" title={title} live="alert">
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** Paste a package and its signature, preview the diff, then apply. Editing either field clears the preview. */
export function NewPackagePage() {
  const navigate = useNavigate();
  const [text, setText] = useState("");
  const [signature, setSignature] = useState("");
  const [errors, setErrors] = useState<{ package?: string; signature?: string }>({});
  const [diff, setDiff] = useState<PackageDiff | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "apply">("");
  const key = useRef(newKey());

  function edited() {
    setDiff(null);
    setFailure(null);
    key.current = newKey();
  }

  function parse() {
    const parsed = parsePackageInput(text, signature);
    setErrors(parsed.errors);
    return parsed.errors.package || parsed.errors.signature ? undefined : parsed.pkg;
  }

  async function preview(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDiff(null);
    const pkg = parse();
    if (pkg === undefined) return;
    setBusy("preview");
    try {
      setDiff(await diffPackage(pkg, signature.trim()));
    } catch (e) {
      setFailure({ title: "Could not preview the package", problem: describeProblem(e) });
    } finally {
      setBusy("");
    }
  }

  async function apply() {
    const pkg = parse();
    if (pkg === undefined) return;
    setBusy("apply");
    setFailure(null);
    try {
      const applied = await applyPackage(pkg, signature.trim(), key.current);
      navigate(`/migration/packages/${applied.packageId}`);
    } catch (e) {
      setFailure({ title: "Could not apply the package", problem: describeProblem(e) });
      key.current = newKey();
    } finally {
      setBusy("");
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Apply configuration package" description="The signature and every file hash are verified first. Preview shows what would be created or updated; nothing is written until you apply." />
      <Card>
        <form className={styles.form} onSubmit={preview} noValidate>
          <div className={styles.wide}>
            <FormField id="package-json" label="Package JSON" error={errors.package}>
              {(control) => (
                <Textarea
                  {...control}
                  mono
                  rows={14}
                  value={text}
                  invalid={Boolean(errors.package)}
                  onChange={(event) => {
                    setText(event.target.value);
                    edited();
                  }}
                />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField id="package-signature" label="Signature (base64)" error={errors.signature}>
              {(control) => (
                <Input
                  {...control}
                  value={signature}
                  invalid={Boolean(errors.signature)}
                  onChange={(event) => {
                    setSignature(event.target.value);
                    edited();
                  }}
                />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <ProblemAlert title={failure.title} problem={failure.problem} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant={diff ? "secondary" : "primary"} loading={busy === "preview"}>
              Preview changes
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration/packages">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {diff ? (
        <Card title={`Preview of version ${diff.version}`}>
          <div className={styles.stack}>
            <StatGroup label="Preview counts">
              <StatTile label="To create" value={diff.counts.create} />
              <StatTile label="To update" value={diff.counts.update} />
              <StatTile label="Unchanged" value={diff.counts.unchanged} />
            </StatGroup>
            <DataTable label="Files in apply order" columns={diffColumns} rows={diff.files} getRowKey={(row) => row.name} />
            {diff.counts.create + diff.counts.update === 0 ? <Alert tone="info" title="Nothing to apply">The target already holds every record in this package.</Alert> : null}
            <div className={styles.formActions}>
              <Button variant="primary" loading={busy === "apply"} onClick={apply}>
                Apply package
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}

const fileColumns: DataTableColumn<ManifestFile>[] = [
  { key: "name", header: "File", mono: true },
  { key: "sha256", header: "SHA-256", mono: true, render: (row) => row.sha256.slice(0, 12) },
  { key: "dependsOn", header: "Depends on", render: (row) => (row.dependsOn.length > 0 ? row.dependsOn.join(", ") : "None") },
];
const historyColumns: DataTableColumn<ConfigPackage["applied"][number]>[] = [
  { key: "appliedAt", header: "Applied", render: (row) => formatDateTime(row.appliedAt) },
  { key: "create", header: "Created", align: "end", render: (row) => row.diffCounts.create },
  { key: "update", header: "Updated", align: "end", render: (row) => row.diffCounts.update },
  { key: "unchanged", header: "Unchanged", align: "end", render: (row) => row.diffCounts.unchanged },
];

/** One applied package: manifest files and the applied history. */
export function PackageDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPackage(id, signal), id);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Configuration package" />
        <Alert tone="danger" title="Could not load this package" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/packages">
          Back to packages
        </Link>
      </div>
    );
  }
  const pkg = state.data?.id === id ? state.data : null;
  if (!pkg) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Configuration package" description="Loading..." />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`Package ${pkg.version}`} description={`Schema ${pkg.schemaVersion}, ${pkg.fileCount} files. Recorded ${formatDateTime(pkg.createdAt)}.`} />
      <Card title="Manifest files">
        <DataTable label="Package files" columns={fileColumns} rows={pkg.files ?? []} getRowKey={(row) => row.name} />
      </Card>
      <Card title="Applied history">
        <DataTable
          label="Applied history"
          columns={historyColumns}
          rows={pkg.applied}
          getRowKey={(row) => row.appliedAt}
          empty={{ title: "Not applied yet", description: "No apply has been recorded for this package." }}
        />
      </Card>
      <Link className={buttonClassName("ghost")} to="/migration/packages">
        Back to packages
      </Link>
    </div>
  );
}
