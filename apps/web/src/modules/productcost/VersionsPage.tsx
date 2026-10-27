import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  activateStoredVersion,
  createStoredVersion,
  describeProblem,
  listStoredVersions,
  transitionStoredVersion,
  type FallbackPrinciple,
  type Problem,
  type StoredVersion,
  type StoredVersionInput,
  type VersionStatus,
  type VersionType,
} from "./api/client";
import { dash } from "./format";
import { ProblemAlert } from "./shared";
import { isUuid, useIdempotencyKey } from "./stored";
import styles from "./pages.module.css";

/** Every lifecycle move a user can request; the service decides whether it is allowed (BR-39.2). */
export type Move = "block" | "unblock" | "activate" | "close";

const moveLabels: Record<Move, string> = { block: "Block", unblock: "Unblock", activate: "Activate", close: "Close" };

/** The moves the state machine offers from a status; Simulation versions never activate (CST-0711). */
export function movesFor(version: Pick<StoredVersion, "type" | "status">): Move[] {
  switch (version.status) {
    case "Open":
      return version.type === "Simulation" ? ["block"] : ["block", "activate"];
    case "Blocked":
      return version.type === "Simulation" ? ["unblock"] : ["unblock", "activate"];
    case "Active":
      return ["close"];
    default:
      return [];
  }
}

const statusTone: Record<VersionStatus, BadgeTone> = { Open: "info", Blocked: "warning", Active: "success", Closed: "neutral" };

export interface VersionValues {
  legalEntityId: string;
  code: string;
  type: VersionType;
  fallback: FallbackPrinciple;
  fallbackVersionId: string;
  sites: string;
  effectiveFrom: string;
}

const emptyValues: VersionValues = {
  legalEntityId: "",
  code: "",
  type: "Standard",
  fallback: "Active",
  fallbackVersionId: "",
  sites: "",
  effectiveFrom: "",
};

/** Checks a new version the way the service will (FR-39.3) and builds the request. */
export function validateVersion(values: VersionValues): { errors: Record<string, string>; input?: StoredVersionInput } {
  const errors: Record<string, string> = {};
  const legalEntityId = values.legalEntityId.trim();
  if (!isUuid(legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  const code = values.code.trim();
  if (code === "") errors.code = "Enter a version code.";
  else if ([...code].length > 30) errors.code = "Use at most 30 characters.";
  if (values.fallback === "SpecifiedVersion" && values.fallbackVersionId === "") errors.fallbackVersionId = "Choose the version to fall back to.";
  const siteIds = values.sites
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  if (siteIds.some((s) => !isUuid(s))) errors.sites = "Enter site ids (UUIDs) separated by commas.";
  else if (new Set(siteIds.map((s) => s.toLowerCase())).size !== siteIds.length) errors.sites = "List each site once.";
  if (values.effectiveFrom !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(values.effectiveFrom)) errors.effectiveFrom = "Enter a date.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId,
      code,
      type: values.type,
      fallback: values.fallback,
      ...(values.fallback === "SpecifiedVersion" ? { fallbackVersionId: values.fallbackVersionId } : {}),
      ...(siteIds.length > 0 ? { siteIds } : {}),
      ...(values.effectiveFrom !== "" ? { effectiveFrom: `${values.effectiveFrom}T00:00:00Z` } : {}),
    },
  };
}

const PAGE_SIZE = 50;

interface ListState {
  items: StoredVersion[];
  next?: string;
  loading: boolean;
  error: Problem | null;
}

/**
 * Costing versions (FR-39.3, BR-39.2), stored per legal entity: list, create
 * and move through Open ⇄ Blocked → Active → Closed. Every move names the
 * version counter the page read, so a version someone else changed meanwhile
 * is refused (CST-0714) instead of being overwritten.
 */
export function VersionsPage() {
  const [statusFilter, setStatusFilter] = useState<VersionStatus | "">("");
  const [list, setList] = useState<ListState>({ items: [], loading: true, error: null });
  const [values, setValues] = useState<VersionValues>(emptyValues);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const idempotency = useIdempotencyKey();
  const latest = useRef(0);

  function load(after?: string) {
    const token = ++latest.current;
    setList((s) => ({ ...s, loading: true, error: null }));
    listStoredVersions({ status: statusFilter || undefined, after, limit: PAGE_SIZE }).then(
      (page) => {
        if (token !== latest.current) return;
        setList((s) => ({ items: after ? [...s.items, ...page.items] : page.items, next: page.next, loading: false, error: null }));
      },
      (err: unknown) => {
        if (token !== latest.current) return;
        setList((s) => ({ ...s, loading: false, error: describeProblem(err) }));
      },
    );
  }

  useEffect(() => {
    load();
  }, [statusFilter]);

  const replace = (v: StoredVersion) => setList((s) => ({ ...s, items: s.items.map((x) => (x.id === v.id ? v : x)) }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateVersion(values);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!input) return;
    setSaving(true);
    try {
      const created = await createStoredVersion(input, idempotency.keyFor(input));
      idempotency.forget();
      setNotice(`Version ${created.code} created, status Open.`);
      setValues({ ...emptyValues, legalEntityId: values.legalEntityId });
      load();
    } catch (err) {
      setFailure({ title: "The version was not created", problem: describeProblem(err) });
    } finally {
      setSaving(false);
    }
  }

  async function move(v: StoredVersion, action: Move) {
    setBusy(`${v.id}:${action}`);
    setFailure(null);
    setNotice(null);
    try {
      const next = action === "activate" ? await activateStoredVersion(v.id, v.version) : await transitionStoredVersion(v.id, action, v.version);
      replace(next);
      setNotice(`Version ${next.code} is now ${next.status}.`);
    } catch (err) {
      const problem = describeProblem(err);
      setFailure({ title: `${moveLabels[action]} ${v.code} was refused`, problem });
      if (problem.code === "CST-0714") load();
    } finally {
      setBusy(null);
    }
  }

  const sameEntity = list.items.filter((v) => v.legalEntityId.toLowerCase() === values.legalEntityId.trim().toLowerCase());
  const codeOf = (id?: string) => list.items.find((v) => v.id === id)?.code ?? id ?? dash;

  const columns: DataTableColumn<StoredVersion>[] = [
    { key: "code", header: "Version", mono: true },
    { key: "type", header: "Type" },
    { key: "status", header: "Status", render: (v) => <Badge tone={statusTone[v.status]}>{v.status}</Badge> },
    { key: "fallback", header: "Fallback", render: (v) => (v.fallback === "SpecifiedVersion" ? `Version ${codeOf(v.fallbackVersionId)}` : v.fallback) },
    { key: "siteIds", header: "Sites", render: (v) => (v.siteIds.length > 0 ? `${v.siteIds.length} sites` : "All sites") },
    { key: "effectiveFrom", header: "Effective from", render: (v) => v.effectiveFrom?.slice(0, 10) ?? dash },
    { key: "activatedAt", header: "Activated", render: (v) => v.activatedAt?.slice(0, 10) ?? dash },
    { key: "legalEntityId", header: "Legal entity", mono: true, render: (v) => v.legalEntityId.slice(0, 8) },
    {
      key: "actions",
      header: "Actions",
      render: (v) => {
        const moves = movesFor(v);
        if (moves.length === 0) return dash;
        return (
          <div className={styles.formActions}>
            {moves.map((m) => (
              <Button
                key={m}
                size="sm"
                variant={m === "activate" ? "primary" : "secondary"}
                loading={busy === `${v.id}:${m}`}
                disabled={busy !== null && busy !== `${v.id}:${m}`}
                onClick={() => void move(v, m)}
                aria-label={`${moveLabels[m]} ${v.code}`}
              >
                {moveLabels[m]}
              </Button>
            ))}
          </div>
        );
      },
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Costing versions"
        description="Standard, planned, simulation and conversion versions per legal entity. A version moves Open ⇄ Blocked, then Open or Blocked → Active, then Active → Closed; simulation versions never activate. Blocking, unblocking and closing need CostingMaintain; activating needs CostingActivate."
      />
      <Card title="New version" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="New costing version">
          <FormField id="version-legal-entity" label="Legal entity" hint="The legal entity's id." error={errors.legalEntityId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.legalEntityId}
                onChange={(e) => setValues({ ...values, legalEntityId: e.target.value, fallbackVersionId: "" })}
                invalid={Boolean(errors.legalEntityId)}
              />
            )}
          </FormField>
          <FormField id="version-code" label="Version code" hint="Unique within the legal entity." error={errors.code}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.code}
                onChange={(e) => setValues({ ...values, code: e.target.value })}
                invalid={Boolean(errors.code)}
              />
            )}
          </FormField>
          <FormField id="version-type" label="Type">
            {(control) => (
              <Select {...control} value={values.type} onChange={(e) => setValues({ ...values, type: e.target.value as VersionType })}>
                <option value="Standard">Standard</option>
                <option value="Planned">Planned</option>
                <option value="Simulation">Simulation</option>
                <option value="Conversion">Conversion</option>
              </Select>
            )}
          </FormField>
          <FormField id="version-fallback" label="Fallback principle" hint="Where a missing price comes from (BR-39.4).">
            {(control) => (
              <Select
                {...control}
                value={values.fallback}
                onChange={(e) => setValues({ ...values, fallback: e.target.value as FallbackPrinciple, fallbackVersionId: "" })}
              >
                <option value="Active">Active version</option>
                <option value="None">None</option>
                <option value="SpecifiedVersion">Specified version</option>
              </Select>
            )}
          </FormField>
          {values.fallback === "SpecifiedVersion" ? (
            <FormField
              id="version-fallback-id"
              label="Fallback version"
              hint={sameEntity.length === 0 ? "No version of this legal entity is listed yet." : "A version of the same legal entity."}
              error={errors.fallbackVersionId}
            >
              {(control) => (
                <Select
                  {...control}
                  value={values.fallbackVersionId}
                  invalid={Boolean(errors.fallbackVersionId)}
                  onChange={(e) => setValues({ ...values, fallbackVersionId: e.target.value })}
                >
                  <option value="">Choose a version</option>
                  {sameEntity.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.code} ({v.status})
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          <FormField id="version-sites" label="Sites" hint="Site ids separated by commas; empty means every site." error={errors.sites} optional>
            {(control) => (
              <Input {...control} value={values.sites} onChange={(e) => setValues({ ...values, sites: e.target.value })} invalid={Boolean(errors.sites)} />
            )}
          </FormField>
          <FormField id="version-effective" label="Effective from" error={errors.effectiveFrom} optional>
            {(control) => (
              <Input
                {...control}
                type="date"
                value={values.effectiveFrom}
                onChange={(e) => setValues({ ...values, effectiveFrom: e.target.value })}
                invalid={Boolean(errors.effectiveFrom)}
              />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving}>
              Create version
            </Button>
          </div>
        </form>
      </Card>
      {notice ? <Alert tone="success" live="status" title={notice} /> : null}
      {failure ? <ProblemAlert title={failure.title} problem={failure.problem} /> : null}
      <Card title="Versions" headingLevel={3}>
        <div className={styles.cardBody}>
          <div className={styles.form}>
            <FormField id="version-status-filter" label="Status">
              {(control) => (
                <Select {...control} value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as VersionStatus | "")}>
                  <option value="">Every status</option>
                  <option value="Open">Open</option>
                  <option value="Blocked">Blocked</option>
                  <option value="Active">Active</option>
                  <option value="Closed">Closed</option>
                </Select>
              )}
            </FormField>
          </div>
          {list.error ? (
            <Alert tone="danger" title="Could not load costing versions" live="alert">
              {list.error.message} ({list.error.code})
            </Alert>
          ) : null}
          <DataTable
            label="Costing versions"
            columns={columns}
            rows={list.items}
            getRowKey={(v) => v.id}
            loading={list.loading && list.items.length === 0}
            empty={{ title: "No costing versions yet", description: "Create a version, then block, activate or close it here." }}
          />
          {list.next ? (
            <div className={styles.formActions}>
              <Button onClick={() => load(list.next)} loading={list.loading}>
                Load more versions
              </Button>
            </div>
          ) : null}
        </div>
      </Card>
    </div>
  );
}
