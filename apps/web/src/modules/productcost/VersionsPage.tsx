import { useState, type FormEvent } from "react";
import { Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  activateVersion,
  describeProblem,
  transitionVersion,
  type CostingVersion,
  type FallbackPrinciple,
  type Problem,
  type VersionStatus,
  type VersionType,
} from "./api/client";
import { dash } from "./format";
import { ProblemAlert } from "./shared";
import styles from "./pages.module.css";

/** Every lifecycle move a user can request; the service decides whether it is allowed (BR-39.2). */
type Move = "block" | "unblock" | "activate" | "close";

const moveLabels: Record<Move, string> = { block: "Block", unblock: "Unblock", activate: "Activate", close: "Close" };

/** The moves the state machine offers from a status; Simulation versions never activate (CST-0711). */
export function movesFor(version: CostingVersion): Move[] {
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

interface Values {
  id: string;
  type: VersionType;
  fallback: FallbackPrinciple;
  fallbackVersionId: string;
  sites: string;
}

const emptyValues: Values = { id: "", type: "Standard", fallback: "Active", fallbackVersionId: "", sites: "" };

/** Checks the new version the way the service will and builds it in status Open. */
export function validateVersion(values: Values, existing: CostingVersion[]): { errors: Record<string, string>; version?: CostingVersion } {
  const errors: Record<string, string> = {};
  const id = values.id.trim();
  if (id === "") errors["version.id"] = "Enter a version code.";
  else if (existing.some((v) => v.id === id)) errors["version.id"] = "A version with this code is already in the list.";
  const fallbackVersionId = values.fallbackVersionId.trim();
  if (values.fallback === "SpecifiedVersion") {
    if (fallbackVersionId === "") errors["version.fallbackVersionId"] = "Name the version to fall back to.";
    else if (fallbackVersionId === id) errors["version.fallbackVersionId"] = "A version cannot fall back to itself.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const siteIds = values.sites
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  return {
    errors,
    version: {
      id,
      type: values.type,
      status: "Open",
      fallback: values.fallback,
      ...(values.fallback === "SpecifiedVersion" ? { fallbackVersionId } : {}),
      ...(siteIds.length > 0 ? { siteIds } : {}),
    },
  };
}

/**
 * Costing versions (FR-39.3, BR-39.2). The service applies the lifecycle rules
 * but does not store versions yet, so the list is kept for this session.
 */
export function VersionsPage() {
  const [versions, setVersions] = useState<CostingVersion[]>([]);
  const [values, setValues] = useState<Values>(emptyValues);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);

  function add(event: FormEvent) {
    event.preventDefault();
    const { errors: found, version } = validateVersion(values, versions);
    setErrors(found);
    setFailure(null);
    if (!version) return;
    setVersions([...versions, version]);
    setValues(emptyValues);
  }

  async function move(version: CostingVersion, action: Move) {
    setBusy(`${version.id}:${action}`);
    setFailure(null);
    try {
      const res = action === "activate" ? await activateVersion({ version }) : await transitionVersion({ version, action });
      setVersions((list) => list.map((v) => (v.id === version.id ? res.version : v)));
    } catch (err) {
      setFailure({ title: `${moveLabels[action]} ${version.id} was refused`, problem: describeProblem(err) });
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<CostingVersion>[] = [
    { key: "id", header: "Version", mono: true },
    { key: "type", header: "Type" },
    { key: "status", header: "Status", render: (v) => <Badge tone={statusTone[v.status]}>{v.status}</Badge> },
    {
      key: "fallback",
      header: "Fallback",
      render: (v) => (v.fallback === "SpecifiedVersion" ? `Version ${v.fallbackVersionId ?? dash}` : v.fallback),
    },
    { key: "siteIds", header: "Sites", render: (v) => (v.siteIds && v.siteIds.length > 0 ? v.siteIds.join(", ") : "All sites") },
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
                aria-label={`${moveLabels[m]} ${v.id}`}
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
        description="A version moves Open ⇄ Blocked, then Open or Blocked → Active, then Active → Closed. Simulation versions never activate. Blocking, unblocking and closing need CostingMaintain; activating needs CostingActivate. Versions are not stored yet, so this list lasts for the session."
      />
      <Card title="New version" headingLevel={3}>
        <form className={styles.form} onSubmit={add} noValidate>
          <FormField id="version-id" label="Version code" error={errors["version.id"]}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.id}
                onChange={(e) => setValues({ ...values, id: e.target.value })}
                invalid={Boolean(errors["version.id"])}
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
                onChange={(e) => setValues({ ...values, fallback: e.target.value as FallbackPrinciple })}
              >
                <option value="Active">Active version</option>
                <option value="None">None</option>
                <option value="SpecifiedVersion">Specified version</option>
              </Select>
            )}
          </FormField>
          {values.fallback === "SpecifiedVersion" ? (
            <FormField id="version-fallback-id" label="Fallback version" error={errors["version.fallbackVersionId"]}>
              {(control) => (
                <Input
                  {...control}
                  className={styles.mono}
                  value={values.fallbackVersionId}
                  onChange={(e) => setValues({ ...values, fallbackVersionId: e.target.value })}
                  invalid={Boolean(errors["version.fallbackVersionId"])}
                />
              )}
            </FormField>
          ) : null}
          <FormField id="version-sites" label="Sites" hint="Comma-separated site ids; empty means all sites." optional>
            {(control) => <Input {...control} value={values.sites} onChange={(e) => setValues({ ...values, sites: e.target.value })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Add version
            </Button>
          </div>
        </form>
      </Card>
      {failure ? <ProblemAlert title={failure.title} problem={failure.problem} /> : null}
      {versions.length === 0 ? (
        <Card>
          <EmptyState
            icon="box"
            headingLevel={3}
            title="No costing versions yet"
            description="Add a version to block, activate or close it. Each move is checked by the costing service."
          />
        </Card>
      ) : (
        <DataTable label="Costing versions" columns={columns} rows={versions} getRowKey={(v) => v.id} />
      )}
    </div>
  );
}
