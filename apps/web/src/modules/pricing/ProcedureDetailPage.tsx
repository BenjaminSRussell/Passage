import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { activateProcedure, describeProblem, getProcedure, newKey, PricingApiError, reviseProcedure, type Procedure, type ProcedureStep, type Problem } from "./api/client";
import { PROCEDURE_STATUSES, STEP_TYPE_LABELS } from "./campaignFormat";
import styles from "./pages.module.css";
import { cleanSteps, ProcedureStepsEditor, validateSteps } from "./ProcedureStepsEditor";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const stepColumns: DataTableColumn<ProcedureStep>[] = [
  { key: "stepNo", header: "Step", mono: true, align: "end" },
  { key: "conditionType", header: "Condition type", render: (row) => STEP_TYPE_LABELS[row.conditionType] ?? row.conditionType },
  { key: "requirementCel", header: "Requirement", mono: true, render: (row) => row.requirementCel || "Always" },
  { key: "exclusionGroup", header: "Exclusion", render: (row) => (row.exclusionGroup ? `${row.exclusionGroup} (${row.exclusionMode === "FIRST" ? "first" : "best"})` : "None") },
  { key: "statistical", header: "Statistical", render: (row) => (row.statistical ? "Yes" : "No") },
  { key: "accountKey", header: "Account key", mono: true },
];

/** One procedure version: its steps; a DRAFT can be changed and activated, other versions are read-only (PRC-0008). */
export function ProcedureDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getProcedure(id, signal), id);
  const [changed, setChanged] = useState<Procedure | null>(null);
  const [draft, setDraft] = useState<ProcedureStep[] | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState<"save" | "activate" | null>(null);
  const reviseKey = useRef(newKey());
  const activateKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Pricing procedure" />
        <Alert tone="danger" title="Could not load the procedure" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Pricing procedure" />
        <span className="visually-hidden">Loading the procedure…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = state.data;
  // A version returned by revise or activate belongs to this id only and is newer than the loaded one.
  const proc = changed && changed.id === id && (changed.revision > loaded.revision || changed.status !== loaded.status) ? changed : loaded;
  const steps = draft ?? proc.steps;
  const status = PROCEDURE_STATUSES[proc.status];

  function refused(title: string, error: unknown, key: { current: string }) {
    const problem = describeProblem(error);
    setFailure({ title: problem.code === "PRC-0008" ? "This version can no longer change" : title, problem });
    // A refused command is final; only a network or server failure may be retried with the same key.
    if (!(error instanceof PricingApiError) || error.status < 500) key.current = newKey();
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const found = validateSteps(steps);
    setErrors(found);
    setFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setBusy("save");
    try {
      const next = await reviseProcedure(proc.id, proc.revision, cleanSteps(steps), reviseKey.current);
      setChanged(next);
      setDraft(null);
      setDone(`Draft saved (revision ${next.revision}).`);
      reviseKey.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) {
        setErrors(problem.fields);
        reviseKey.current = newKey();
      } else {
        refused(problem.code === "conflict_version" ? "Someone else changed this draft; reload it" : "Could not save the draft", error, reviseKey);
      }
    } finally {
      setBusy(null);
    }
  }

  async function activate() {
    setFailure(null);
    setDone("");
    if (draft) {
      setFailure({ title: "Save the draft first", problem: { code: "COM-0001", message: "The steps have unsaved changes.", fields: {} } });
      return;
    }
    setBusy("activate");
    try {
      const next = await activateProcedure(proc.id, activateKey.current);
      setChanged(next);
      setDone(`Version ${next.version} of ${next.name} is active; the previous active version is superseded.`);
      activateKey.current = newKey();
    } catch (error) {
      refused("Could not activate the version", error, activateKey);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${proc.name} version ${proc.version}`}
        description={`Legal entity ${proc.legalEntityId}`}
        meta={<Badge tone={status.tone}>{status.label}</Badge>}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/pricing/procedures/new?from=${encodeURIComponent(proc.id)}`}>
              New version from this
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/procedures">
              All procedures
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          {failure.problem.message} ({failure.problem.code})
        </Alert>
      ) : null}
      {proc.status === "DRAFT" ? (
        <>
          <Card title="Steps">
            <form className={styles.form} onSubmit={save} noValidate>
              <ProcedureStepsEditor
                idPrefix="pd-step"
                steps={steps}
                errors={errors}
                onChange={(next) => {
                  setDraft(next);
                  reviseKey.current = newKey();
                }}
              />
              <div className={styles.formActions}>
                <Button type="submit" variant="secondary" loading={busy === "save"} disabled={!draft}>
                  Save draft
                </Button>
              </div>
            </form>
          </Card>
          <Card title="Activate">
            <div className={styles.stack}>
              <p className={styles.note}>Activating makes this version the active {proc.name} and supersedes the active one. An active version cannot change; changes need a new version.</p>
              <div className={styles.actions}>
                <Button variant="primary" loading={busy === "activate"} onClick={activate}>
                  Activate version {proc.version}
                </Button>
              </div>
            </div>
          </Card>
        </>
      ) : (
        <Card title="Steps">
          <DataTable label="Procedure steps" columns={stepColumns} rows={proc.steps} getRowKey={(row) => String(row.stepNo)} empty={{ title: "No steps" }} />
        </Card>
      )}
    </div>
  );
}
