import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, listLegalEntities, listOperatingUnits, listSites, listWarehouses, type Problem } from "./api/client";
import {
  deleteNameTranslation,
  listNameTranslations,
  ownerKinds,
  putNameTranslation,
  resolveName,
  type NameResolution,
  type NameTranslation,
  type OwnerCollection,
} from "./api/translations";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { FilterSelect, useRows } from "./referenceParts";
import { ProblemMessage, problemSummary } from "../../shell/errors";

const languagePattern = /^[a-z]{2}(-[A-Z]{2})?$/;
const maxNameLength = 120;

const messages: Record<string, string> = {
  "ORG-TRANSLATION-LANGUAGE-INVALID": "That language is not an active language for this tenant.",
  "ORG-TRANSLATION-NOT-FOUND": "That translation no longer exists.",
};

/** Plain wording for a problem; `ProblemMessage` keeps its code under Details. */
const formatProblem = (problem: Problem) => messages[problem.code] ?? problemSummary(problem);

export function validateTranslation(language: string, name: string): { language?: string; name?: string } {
  const errors: { language?: string; name?: string } = {};
  if (!languagePattern.test(language)) {
    errors.language = "Enter a language tag such as fr or fr-CA.";
  }
  const length = [...name.trim()].length;
  if (length === 0 || length > maxNameLength) {
    errors.name = "Enter a name of 1 to 120 characters.";
  }
  return errors;
}

interface OwnerOption {
  id: string;
  label: string;
}

async function loadOwners(collection: OwnerCollection, scope: string, signal: AbortSignal): Promise<OwnerOption[]> {
  switch (collection) {
    case "legal-entities":
      return (await listLegalEntities(scope, { signal })).items.map((row) => ({ id: row.id, label: `${row.code} - ${row.name}` }));
    case "operating-units":
      return (await listOperatingUnits(scope, { signal })).items.map((row) => ({ id: row.id, label: `${row.code} - ${row.name}` }));
    case "sites":
      return (await listSites(scope, { signal })).items.map((row) => ({ id: row.id, label: `${row.code} - ${row.name}` }));
    default:
      return (await listWarehouses(scope, { signal })).items.map((row) => ({ id: row.id, label: `${row.code} - ${row.name}` }));
  }
}

interface EditorProps {
  collection: OwnerCollection;
  ownerId: string;
  scope: string;
  canWrite: boolean;
}

function RemoveTranslation({ row, collection, scope, onRemoved }: { row: NameTranslation; collection: OwnerCollection; scope: string; onRemoved(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  const confirm = async () => {
    setBusy(true);
    setFailure(null);
    try {
      await deleteNameTranslation(collection, row.ownerId, row.language, scope);
      onRemoved();
    } catch (error) {
      setFailure(describeProblem(error));
      setBusy(false);
    }
  };

  return (
    <div className={styles.rowActions}>
      {confirming ? (
        <>
          <Button size="sm" variant="danger" loading={busy} aria-label={`Confirm remove ${row.language} name`} onClick={confirm}>
            Confirm
          </Button>
          <Button size="sm" variant="ghost" aria-label={`Cancel remove ${row.language} name`} onClick={() => setConfirming(false)}>
            Cancel
          </Button>
        </>
      ) : (
        <Button size="sm" aria-label={`Remove ${row.language} name`} onClick={() => setConfirming(true)}>
          Remove
        </Button>
      )}
      {failure ? (
        <span role="alert" className={styles.rowError}>
          <ProblemMessage problem={failure} message={formatProblem(failure)} />
        </span>
      ) : null}
    </div>
  );
}

function PreviewPanel({ collection, ownerId, scope }: Omit<EditorProps, "canWrite">) {
  const [language, setLanguage] = useState("");
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<NameResolution | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = languagePattern.test(language) ? undefined : "Enter a language tag such as fr or fr-CA.";
    setError(found);
    setResult(null);
    setFailure(null);
    if (found) {
      return;
    }
    setBusy(true);
    try {
      setResult(await resolveName(collection, ownerId, language, scope));
    } catch (caught) {
      setFailure(describeProblem(caught));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Preview a language">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="preview-language" label="Preview language" hint="Exact tag first, then its primary language, then the base name." error={error}>
          {(control) => <Input {...control} value={language} onChange={(event) => setLanguage(event.target.value)} />}
        </FormField>
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Preview name
          </Button>
        </div>
      </form>
      <div role="status">
        {result ? (
          <dl className={styles.result}>
            <dt>Name shown</dt>
            <dd>{result.name}</dd>
            <dt>Source</dt>
            <dd>{result.source === "translation" ? `Translation (${result.language})` : "Base name"}</dd>
          </dl>
        ) : null}
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not resolve the name" live="alert">
          <ProblemMessage problem={failure} message={formatProblem(failure)} />
        </Alert>
      ) : null}
    </Card>
  );
}

function TranslationEditor({ collection, ownerId, scope, canWrite }: EditorProps) {
  const rows = useRows((signal) => listNameTranslations(collection, ownerId, scope, { signal }), `${collection}|${ownerId}|${scope}`);
  const [language, setLanguage] = useState("");
  const [name, setName] = useState("");
  const [errors, setErrors] = useState<{ language?: string; name?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateTranslation(language, name);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await putNameTranslation(collection, ownerId, language, name.trim(), scope);
      setLanguage("");
      setName("");
      rows.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-TRANSLATION-LANGUAGE-INVALID") {
        setErrors({ language: formatProblem(problem) });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const columns: DataTableColumn<NameTranslation>[] = [
    { key: "language", header: "Language", mono: true },
    { key: "name", header: "Name" },
    { key: "updatedAt", header: "Updated", render: (row) => row.updatedAt.slice(0, 10) },
  ];
  if (canWrite) {
    columns.push({
      key: "actions",
      header: "Actions",
      render: (row) => (
        <div className={styles.rowActions}>
          <Button
            size="sm"
            aria-label={`Edit ${row.language} name`}
            onClick={() => {
              setLanguage(row.language);
              setName(row.name);
              setErrors({});
              setFailure(null);
            }}
          >
            Edit
          </Button>
          <RemoveTranslation row={row} collection={collection} scope={scope} onRemoved={rows.reload} />
        </div>
      ),
    });
  }

  return (
    <>
      {canWrite ? (
        <Card title="Set a translated name">
          <form onSubmit={submit} noValidate className={styles.form}>
            <FormField id="translation-language" label="Language" hint="A language tag such as fr or fr-CA. Saving an existing language replaces its name." error={errors.language}>
              {(control) => <Input {...control} value={language} onChange={(event) => setLanguage(event.target.value)} maxLength={5} />}
            </FormField>
            <FormField id="translation-name" label="Translated name" error={errors.name}>
              {(control) => <Input {...control} value={name} onChange={(event) => setName(event.target.value)} />}
            </FormField>
            {failure ? (
              <Alert tone="danger" title="Could not save the translation" live="alert">
                <ProblemMessage problem={failure} message={formatProblem(failure)} />
              </Alert>
            ) : null}
            <div>
              <Button type="submit" variant="primary" loading={busy}>
                Save translation
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {rows.error ? (
        <Alert tone="danger" title="Could not load translations" live="alert">
          <ProblemMessage problem={rows.error} message={formatProblem(rows.error)} />
        </Alert>
      ) : null}
      <DataTable
        label="Name translations"
        columns={columns}
        rows={rows.rows}
        getRowKey={(row) => row.language}
        loading={rows.loading}
        empty={{ title: "No translations", description: "The base name is shown in every language until a translation is set." }}
      />
      <PreviewPanel collection={collection} ownerId={ownerId} scope={scope} />
    </>
  );
}

export function NameTranslationsPage() {
  const { scope, loading, canWriteLegalEntity, canWriteOperatingUnit, canWriteSite } = useLegalEntity();
  const [collection, setCollection] = useState<OwnerCollection>("legal-entities");
  const [ownerId, setOwnerId] = useState("");
  const owners = useRows((signal) => (scope === "" ? Promise.resolve([]) : loadOwners(collection, scope, signal)), `${collection}|${scope}`);

  const dutyHeld = collection === "legal-entities" ? canWriteLegalEntity : collection === "operating-units" ? canWriteOperatingUnit : canWriteSite;
  const canWrite = dutyHeld && scope !== ALL && scope !== "";
  const selected = owners.rows.some((owner) => owner.id === ownerId) ? ownerId : "";

  return (
    <>
      <PageHeader title="Name translations" description="Translated names of legal entities, operating units, sites and warehouses for documents and lists." />
      <div className={styles.filters}>
        <FilterSelect
          id="translation-kind"
          label="Record type"
          value={collection}
          options={ownerKinds.map((kind) => ({ value: kind.collection, label: kind.label }))}
          onChange={(value) => {
            setCollection(value as OwnerCollection);
            setOwnerId("");
          }}
        />
        <FilterSelect
          id="translation-owner"
          label="Record"
          value={selected}
          options={[{ value: "", label: owners.loading ? "Loading" : "Select a record" }, ...owners.rows.map((owner) => ({ value: owner.id, label: owner.label }))]}
          onChange={setOwnerId}
        />
      </div>
      {owners.error ? (
        <Alert tone="danger" title="Could not load records" live="alert">
          <ProblemMessage problem={owners.error} message={formatProblem(owners.error)} />
        </Alert>
      ) : null}
      {dutyHeld && scope === ALL ? (
        <Alert tone="info" title="Viewing all entities">
          Choose one legal entity in the switcher to change translated names.
        </Alert>
      ) : null}
      {selected === "" ? (
        <DataTable
          label="Name translations"
          columns={[{ key: "language", header: "Language" }]}
          rows={[]}
          getRowKey={(row: { language: string }) => row.language}
          loading={loading || (owners.loading && scope !== "")}
          empty={{ title: "Select a record", description: "Choose a record type and a record to see its translated names." }}
        />
      ) : (
        <TranslationEditor key={`${collection}|${selected}`} collection={collection} ownerId={selected} scope={scope} canWrite={canWrite} />
      )}
    </>
  );
}
