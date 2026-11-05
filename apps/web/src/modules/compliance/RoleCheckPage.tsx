import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, evaluate, simulate, type Conflict, type Problem, type Role, type SimulatedConflict, type SimulateResult } from "./api/client";
import { sentence, severityTone, stateTone, UUID } from "./format";
import styles from "./pages.module.css";

/**
 * Parses one role per line, "RoleName: DutyA, DutyB". Blank lines are
 * skipped; a line without a colon or without duties is an error.
 */
export function parseRoles(text: string): { roles: Role[]; error?: string } {
  const roles: Role[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    const colon = line.indexOf(":");
    const name = colon > 0 ? line.slice(0, colon).trim() : "";
    const duties = colon > 0 ? line.slice(colon + 1).split(",").map((d) => d.trim()).filter(Boolean) : [];
    if (!name || duties.length === 0) return { roles: [], error: `Line ${i + 1}: write "Role: Duty, Duty".` };
    roles.push({ name, duties });
  }
  return { roles };
}

interface Values {
  userId: string;
  current: string;
  proposed: string;
}

type Errors = Partial<Record<keyof Values, string>>;

function check(values: Values, needProposed: boolean): { errors: Errors; current: Role[]; proposed: Role[] } {
  const errors: Errors = {};
  if (!UUID.test(values.userId.trim())) errors.userId = "Enter the user's UUID.";
  const current = parseRoles(values.current);
  const proposed = parseRoles(values.proposed);
  if (current.error) errors.current = current.error;
  if (proposed.error) errors.proposed = proposed.error;
  else if (needProposed && proposed.roles.length === 0) errors.proposed = "Add at least one role to assign.";
  return { errors, current: current.roles, proposed: proposed.roles };
}

const simColumns: DataTableColumn<SimulatedConflict>[] = [
  { key: "ruleId", header: "Rule", mono: true },
  { key: "severity", header: "Severity", render: (c) => <Badge tone={severityTone[c.severity]}>{sentence(c.severity)}</Badge> },
  { key: "duties", header: "Duties", render: (c) => `${c.dutyA} and ${c.dutyB}` },
  { key: "roles", header: "Roles", render: (c) => [...new Set([...c.rolesA, ...c.rolesB])].join(", ") },
  { key: "state", header: "Recorded state", render: (c) => <Badge tone={stateTone[c.state]}>{sentence(c.state)}</Badge> },
];

const evalColumns: DataTableColumn<Conflict>[] = [
  { key: "ruleId", header: "Rule", mono: true, render: (c) => <Link to={`/compliance/sod/conflicts/${c.conflictId}`}>{c.ruleId}</Link> },
  { key: "severity", header: "Severity", render: (c) => <Badge tone={severityTone[c.severity]}>{sentence(c.severity)}</Badge> },
  { key: "duties", header: "Duties", render: (c) => `${c.dutyA} and ${c.dutyB}` },
  { key: "state", header: "State", render: (c) => <Badge tone={stateTone[c.state]}>{sentence(c.state)}</Badge> },
];

/**
 * Role assignment check (PRD §9.48.6, compliance.security.admin): simulate an assignment
 * before it is saved (FR-48.5, sod.block_high), or record an evaluation of a
 * user's current effective roles so conflicts reach the review queue.
 */
export function RoleCheckPage() {
  const [values, setValues] = useState<Values>({ userId: "", current: "", proposed: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState<"simulate" | "evaluate" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<SimulateResult | null>(null);
  const [recorded, setRecorded] = useState<Conflict[] | null>(null);

  function field(name: keyof Values, patch: string) {
    setValues((v) => ({ ...v, [name]: patch }));
  }

  async function runSimulate(event: FormEvent) {
    event.preventDefault();
    const { errors: found, current, proposed } = check(values, true);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy("simulate");
    setFailure(null);
    setRecorded(null);
    try {
      setResult(await simulate({ userId: values.userId.trim(), currentRoles: current, proposedRoles: proposed }));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(null);
    }
  }

  async function runEvaluate() {
    const { errors: found, current } = check(values, false);
    delete found.proposed;
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy("evaluate");
    setFailure(null);
    setResult(null);
    try {
      setRecorded((await evaluate({ userId: values.userId.trim(), roles: current })).conflicts);
    } catch (error) {
      setFailure(describeProblem(error));
      setRecorded(null);
    } finally {
      setBusy(null);
    }
  }

  const summary = result
    ? result.blocked
      ? null
      : result.newConflicts.length === 0
        ? "No new conflicts. The assignment can be saved."
        : `${result.newConflicts.length} new ${result.newConflicts.length === 1 ? "conflict" : "conflicts"}, none blocking. The assignment can be saved.`
    : recorded
      ? `Evaluation recorded: ${recorded.filter((c) => c.state !== "RESOLVED").length} active ${recorded.length === 1 ? "conflict" : "conflicts"} for this user.`
      : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Role assignment check"
        description="Check a role assignment for segregation-of-duties conflicts before you save it. Write one role per line as Role: Duty, Duty."
      />
      <Card>
        <form className={styles.form} onSubmit={runSimulate} noValidate aria-label="Role assignment check">
          <FormField id="check-user" label="User" error={errors.userId}>
            {(control) => <Input {...control} className={styles.mono} value={values.userId} onChange={(e) => field("userId", e.target.value)} invalid={Boolean(errors.userId)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="check-current" label="Current roles" hint="The user's roles today, with their duties." error={errors.current}>
              {(control) => <Textarea {...control} className={styles.mono} rows={4} value={values.current} onChange={(e) => field("current", e.target.value)} invalid={Boolean(errors.current)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField id="check-proposed" label="Roles to assign" hint="Needed to simulate; ignored when you record an evaluation." error={errors.proposed}>
              {(control) => <Textarea {...control} className={styles.mono} rows={3} value={values.proposed} onChange={(e) => field("proposed", e.target.value)} invalid={Boolean(errors.proposed)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "simulate"} disabled={busy === "evaluate"}>
              Simulate
            </Button>
            <Button loading={busy === "evaluate"} disabled={busy === "simulate"} onClick={() => void runEvaluate()}>
              Record evaluation of current roles
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The check failed" : result?.blocked ? "This assignment is blocked" : undefined} live="alert">
        {failure
          ? `${failure.message} (${failure.code})`
          : result?.blocked
            ? `Unmitigated high-severity conflict on ${result.blockingRuleIds.join(", ")} (${result.code ?? "SOD-1101"}). Mitigate it or remove a role before saving.`
            : null}
      </Alert>
      <Alert tone="success" live="status">
        {summary}
      </Alert>
      {result && result.newConflicts.length > 0 ? (
        <DataTable label="New conflicts" columns={simColumns} rows={result.newConflicts} getRowKey={(c) => c.ruleId} />
      ) : null}
      {recorded && recorded.length > 0 ? (
        <DataTable label="Recorded conflicts" columns={evalColumns} rows={recorded} getRowKey={(c) => c.conflictId} />
      ) : null}
    </div>
  );
}
