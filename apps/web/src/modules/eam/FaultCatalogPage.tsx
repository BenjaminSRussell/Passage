import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getFaultCatalog,
  listFaultCatalogs,
  putFaultCatalog,
  type FaultCatalog,
  type FaultCatalogInput,
  type FaultCatalogSummary,
  type FaultCode,
  type Problem,
} from "./api/client";
import { CODE, FAULT_CODE, minuteOf } from "./format";
import { Failure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Kind = "symptoms" | "areas" | "types" | "causes" | "remedies";

const KINDS: { key: Kind; label: string; example: string }[] = [
  { key: "symptoms", label: "Symptoms", example: "NOISE Abnormal noise" },
  { key: "areas", label: "Areas", example: "BEARING Bearing" },
  { key: "types", label: "Fault types", example: "WEAR Wear" },
  { key: "causes", label: "Causes", example: "FATIGUE Material fatigue" },
  { key: "remedies", label: "Remedies", example: "REPLACE Replace the part" },
];

export interface CatalogText {
  symptoms: string;
  areas: string;
  types: string;
  causes: string;
  remedies: string;
  combinations: string;
}

type CatalogErrors = Partial<Record<keyof CatalogText, string>>;

const lines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

/** A catalog as the designer's text: one "CODE Text" per line, and "SYMPTOM AREA TYPE" per allowed combination. */
export function catalogToText(c: FaultCatalogInput | null): CatalogText {
  const codes = (list: FaultCode[]) => list.map((x) => `${x.code} ${x.text}`).join("\n");
  if (!c) return { symptoms: "", areas: "", types: "", causes: "", remedies: "", combinations: "" };
  return {
    symptoms: codes(c.symptoms),
    areas: codes(c.areas),
    types: codes(c.types),
    causes: codes(c.causes),
    remedies: codes(c.remedies),
    combinations: c.allowedCombinations.map((x) => `${x.symptom} ${x.area} ${x.type}`).join("\n"),
  };
}

/** The checks the service makes on a catalog (domain.CheckCatalog), and the catalog the form sends. */
export function parseCatalog(text: CatalogText): { errors: CatalogErrors; input?: FaultCatalogInput } {
  const errors: CatalogErrors = {};
  const input: FaultCatalogInput = { symptoms: [], areas: [], types: [], causes: [], remedies: [], allowedCombinations: [] };
  for (const kind of KINDS) {
    const seen = new Set<string>();
    const rows = lines(text[kind.key]);
    if (rows.length === 0 || rows.length > 200) errors[kind.key] = `List 1 to 200 ${kind.label.toLowerCase()}, one per line.`;
    for (const row of rows) {
      const m = /^(\S+)\s+(.+)$/.exec(row);
      const code = m?.[1] ?? row;
      const label = m?.[2]?.trim() ?? "";
      if (!m || !FAULT_CODE.test(code) || label.length > 120) {
        errors[kind.key] = `Write each line as a code of up to 20 capital letters, digits or underscores, a space and a text of up to 120 characters, like "${kind.example}".`;
        break;
      }
      if (seen.has(code)) {
        errors[kind.key] = `${code} is listed twice.`;
        break;
      }
      seen.add(code);
      input[kind.key].push({ code, text: label });
    }
  }
  const has = (kind: Kind, code: string) => input[kind].some((c) => c.code === code);
  const combos = lines(text.combinations);
  const seen = new Set<string>();
  if (combos.length === 0 || combos.length > 1000) errors.combinations = "List at least one allowed combination.";
  for (const row of combos) {
    const parts = row.split(/\s+/);
    if (parts.length !== 3) {
      errors.combinations = `Write each combination as "SYMPTOM AREA TYPE", like "NOISE BEARING WEAR".`;
      break;
    }
    const [symptom, area, type] = parts;
    if (!has("symptoms", symptom) || !has("areas", area) || !has("types", type)) {
      errors.combinations = `${row} uses a code that is not listed above.`;
      break;
    }
    if (seen.has(row)) {
      errors.combinations = `${row} is listed twice.`;
      break;
    }
    seen.add(row);
    input.allowedCombinations.push({ symptom, area, type });
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input };
}

interface ComboRow {
  id: string;
  symptom: string;
  area: string;
  type: string;
}

const textOf = (codes: FaultCode[], code: string) => codes.find((c) => c.code === code)?.text ?? code;

const comboColumns = (c: FaultCatalog): DataTableColumn<ComboRow>[] => [
  { key: "symptom", header: "Symptom", render: (row) => `${row.symptom} · ${textOf(c.symptoms, row.symptom)}` },
  { key: "area", header: "Area", render: (row) => `${row.area} · ${textOf(c.areas, row.area)}` },
  { key: "type", header: "Type", render: (row) => `${row.type} · ${textOf(c.types, row.type)}` },
];

/** Fault catalog designer (8.44.6, FR-44.16): codes per kind and the allowed symptom/area/type combinations of one asset type. */
export function FaultCatalogPage() {
  const [params, setParams] = useSearchParams();
  const type = params.get("type") ?? "";
  const [draft, setDraft] = useState(type);
  const [draftError, setDraftError] = useState<string | null>(null);

  function open(event: FormEvent) {
    event.preventDefault();
    const next = draft.trim().toUpperCase();
    if (!CODE.test(next)) {
      setDraftError("Use the asset type code: 2 to 20 capital letters, digits or underscores.");
      return;
    }
    setDraftError(null);
    setParams({ type: next });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Fault catalog designer"
        description="Each asset type has one catalog. Technicians choose a symptom, area and type from its allowed combinations, then a cause and a remedy."
      />
      <Card>
        <form className={styles.toolbar} onSubmit={open} noValidate aria-label="Choose asset type">
          <FormField id="catalog-type" label="Asset type" error={draftError ?? undefined}>
            {(control) => <Input {...control} value={draft} maxLength={20} invalid={Boolean(draftError)} onChange={(e) => setDraft(e.target.value)} />}
          </FormField>
          <Button type="submit">Open catalog</Button>
        </form>
      </Card>
      {type ? <CatalogEditor key={type} assetType={type} /> : <CatalogList onOpen={(assetType) => setParams({ type: assetType })} />}
    </div>
  );
}

const listColumns: DataTableColumn<FaultCatalogSummary & { id: string }>[] = [
  { key: "assetType", header: "Asset type", mono: true },
  { key: "version", header: "Version", align: "end" },
  { key: "codes", header: "Codes", align: "end" },
  { key: "combinations", header: "Allowed combinations", align: "end" },
  { key: "updatedAt", header: "Updated", mono: true, render: (row) => minuteOf(row.updatedAt) },
];

function CatalogList({ onOpen }: { onOpen(assetType: string): void }) {
  const state = useLoad((signal) => listFaultCatalogs(signal), "catalogs");
  return (
    <Card title="Catalogs">
      {state.error ? (
        <Failure title="Could not load the catalogs" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Fault catalogs"
          columns={listColumns}
          rows={(state.data ?? []).map((c) => ({ ...c, id: c.assetType }))}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No fault catalogs yet", description: "Enter an asset type, such as PUMP, to start its catalog." }}
          onRowOpen={(row) => onOpen(row.assetType)}
        />
      )}
    </Card>
  );
}

function CatalogEditor({ assetType }: { assetType: string }) {
  const [round, setRound] = useState(0);
  const state = useLoad((signal) => getFaultCatalog(assetType, signal), `${assetType}|${round}`);
  const [text, setText] = useState<CatalogText>(catalogToText(null));
  const [errors, setErrors] = useState<CatalogErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!state.loading && !state.error) setText(catalogToText(state.data));
  }, [state.data, state.loading, state.error]);

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = parseCatalog(text);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await putFaultCatalog(assetType, input, state.data?.version ?? 0);
      setNotice(`Catalog for ${assetType} saved as version ${saved.version}.`);
      setRound((n) => n + 1);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) return <Failure title="Could not load the catalog" problem={state.error} retry={state.reload} />;
  if (state.loading && state.data === null && round === 0) {
    return (
      <div aria-busy="true">
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const catalog = state.data;

  return (
    <>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title={catalog ? `Allowed combinations for ${assetType} (version ${catalog.version})` : `No catalog for ${assetType} yet`}>
        {catalog ? (
          <DataTable
            label="Allowed combinations"
            columns={comboColumns(catalog)}
            rows={catalog.allowedCombinations.map((c) => ({ ...c, id: `${c.symptom}|${c.area}|${c.type}` }))}
            getRowKey={(row) => row.id}
            empty={{ title: "No combinations" }}
          />
        ) : (
          <p className={styles.note}>Write the codes below and save to create the catalog. Corrective and emergency work orders on {assetType} assets cannot complete until it exists and a fault is registered.</p>
        )}
      </Card>
      <Card title={catalog ? "Edit the catalog" : "New catalog"}>
        <form className={styles.form} onSubmit={save} noValidate aria-label="Fault catalog">
          {KINDS.map((kind) => (
            <FormField key={kind.key} id={`catalog-${kind.key}`} label={kind.label} hint={`One per line, for example "${kind.example}".`} error={errors[kind.key]}>
              {(control) => (
                <Textarea {...control} rows={5} value={text[kind.key]} invalid={Boolean(errors[kind.key])} onChange={(e) => setText({ ...text, [kind.key]: e.target.value })} />
              )}
            </FormField>
          ))}
          <FormField id="catalog-combinations" label="Allowed combinations" hint={`One "SYMPTOM AREA TYPE" per line, for example "NOISE BEARING WEAR".`} error={errors.combinations}>
            {(control) => (
              <Textarea {...control} rows={5} value={text.combinations} invalid={Boolean(errors.combinations)} onChange={(e) => setText({ ...text, combinations: e.target.value })} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title={failure.code === "conflict_version" ? "Someone else changed this catalog. Reload it and try again." : "Could not save the catalog"} problem={failure} retry={failure.code === "conflict_version" ? () => setRound((n) => n + 1) : undefined} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save catalog
            </Button>
          </div>
        </form>
      </Card>
    </>
  );
}
