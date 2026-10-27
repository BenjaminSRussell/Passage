import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { createTestGroup, describeProblem, listTestGroups, newKey, type NewTestGroup, type Problem, type TestGroup, type TestLine } from "./api/client";
import { CODE, SIGNED_DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<TestGroup>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "version", header: "Version", align: "end", render: (row) => `v${row.version}` },
  { key: "tests", header: "Tests", align: "end", render: (row) => String(row.tests.length) },
  { key: "required", header: "Required", align: "end", render: (row) => String(row.tests.filter((t) => t.required).length) },
  {
    key: "kinds",
    header: "Kinds",
    render: (row) => (
      <span className={styles.flags}>
        {row.tests.some((t) => t.kind === "VARIABLE") ? <Badge tone="info">Variable</Badge> : null}
        {row.tests.some((t) => t.kind === "ATTRIBUTE") ? <Badge tone="neutral">Attribute</Badge> : null}
        {row.tests.some((t) => t.destructive) ? <Badge tone="warning">Destructive</Badge> : null}
      </span>
    ),
  },
  { key: "id", header: "Id", mono: true },
];

/** Test groups (FR-33.1): every version is kept; orders reference the version they were created with. */
export function TestGroupsPage() {
  const state = useLoad((signal) => listTestGroups(signal), "test-groups");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Test groups"
        description="The tests a quality order runs, with limits and passing outcomes. Saving a group code again creates its next version; existing orders keep theirs."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/test-groups/new">
            New test group version
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load test groups" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Test groups"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No test groups yet", description: "Create a test group before you set up associations or manual orders." }}
        />
      )}
    </div>
  );
}

interface LineValues {
  name: string;
  kind: "VARIABLE" | "ATTRIBUTE";
  required: boolean;
  destructive: boolean;
  decimals: string;
  min: string;
  max: string;
  passingOutcomeIds: string;
}

const blankLine: LineValues = { name: "", kind: "VARIABLE", required: true, destructive: false, decimals: "2", min: "", max: "", passingOutcomeIds: "" };

/** Checks that mirror the service's ValidateTestGroup, and the request the form sends. testId makes ids (tests). */
export function validateTestGroup(code: string, lines: LineValues[], testId: () => string = () => crypto.randomUUID()): { errors: Record<string, string>; input?: NewTestGroup } {
  const errors: Record<string, string> = {};
  if (!CODE.test(code.trim())) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as INCOMING-STEEL.";
  if (lines.length === 0) errors.code = "Add at least one test.";
  const tests: TestLine[] = [];
  lines.forEach((l, i) => {
    if (l.name.trim() === "") errors[`${i}.name`] = "Name the test.";
    if (!/^[0-6]$/.test(l.decimals.trim())) errors[`${i}.decimals`] = "Enter 0 to 6.";
    const test: TestLine = { testId: testId(), name: l.name.trim(), kind: l.kind, required: l.required, destructive: l.destructive, decimals: Number(l.decimals) };
    if (l.kind === "VARIABLE") {
      for (const bound of ["min", "max"] as const) {
        const v = l[bound].trim();
        if (v !== "" && !SIGNED_DECIMAL.test(v)) errors[`${i}.${bound}`] = "Enter a number, or leave it empty for no limit.";
        else if (v !== "") test[bound] = v;
      }
      if (test.min !== undefined && test.max !== undefined && Number(test.min) > Number(test.max)) errors[`${i}.min`] = "The minimum must not exceed the maximum.";
    } else {
      const outcomes = l.passingOutcomeIds.split(/[\s,]+/).filter(Boolean);
      if (outcomes.length === 0 || outcomes.some((o) => !UUID.test(o))) errors[`${i}.passingOutcomeIds`] = "Enter one or more passing outcome ids (UUIDs), separated by commas.";
      test.passingOutcomeIds = outcomes;
    }
    tests.push(test);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code: code.trim(), tests } };
}

/** Create the next version of a test group code with its tests. */
export function NewTestGroupPage() {
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const [lines, setLines] = useState<LineValues[]>([blankLine]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(i: number, patch: Partial<LineValues>) {
    setLines(lines.map((l, j) => (j === i ? { ...l, ...patch } : l)));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateTestGroup(code, lines);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      await createTestGroup(checked.input, key.current);
      navigate("/quality/test-groups");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.fields.code) setErrors({ code: problem.fields.code });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New test group version" description="Variable tests compare a value to inclusive limits after rounding to the declared decimals. Attribute tests pass on one of their passing outcomes." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New test group version">
          <FormField id="group-code" label="Group code" hint="An existing code gets its next version." error={errors.code}>
            {(control) => <Input {...control} maxLength={32} value={code} invalid={Boolean(errors.code)} onChange={(event) => { setCode(event.target.value); key.current = newKey(); }} />}
          </FormField>
          {lines.map((line, i) => (
            <fieldset key={i} className={styles.valueGroup}>
              <legend className={styles.legend}>{`Test ${i + 1}`}</legend>
              <FormField id={`test-${i}-name`} label="Name" error={errors[`${i}.name`]}>
                {(control) => <Input {...control} value={line.name} invalid={Boolean(errors[`${i}.name`])} onChange={(event) => update(i, { name: event.target.value })} />}
              </FormField>
              <FormField id={`test-${i}-kind`} label="Kind">
                {(control) => (
                  <Select {...control} value={line.kind} onChange={(event) => update(i, { kind: event.target.value as LineValues["kind"] })}>
                    <option value="VARIABLE">Variable (measured value)</option>
                    <option value="ATTRIBUTE">Attribute (outcome)</option>
                  </Select>
                )}
              </FormField>
              {line.kind === "VARIABLE" ? (
                <>
                  <FormField id={`test-${i}-decimals`} label="Decimals" error={errors[`${i}.decimals`]}>
                    {(control) => <Input {...control} inputMode="numeric" value={line.decimals} invalid={Boolean(errors[`${i}.decimals`])} onChange={(event) => update(i, { decimals: event.target.value })} />}
                  </FormField>
                  <FormField id={`test-${i}-min`} label="Minimum" optional error={errors[`${i}.min`]}>
                    {(control) => <Input {...control} inputMode="decimal" value={line.min} invalid={Boolean(errors[`${i}.min`])} onChange={(event) => update(i, { min: event.target.value })} />}
                  </FormField>
                  <FormField id={`test-${i}-max`} label="Maximum" optional error={errors[`${i}.max`]}>
                    {(control) => <Input {...control} inputMode="decimal" value={line.max} invalid={Boolean(errors[`${i}.max`])} onChange={(event) => update(i, { max: event.target.value })} />}
                  </FormField>
                </>
              ) : (
                <FormField id={`test-${i}-outcomes`} label="Passing outcome ids" hint="Comma-separated UUIDs from the outcome catalog." error={errors[`${i}.passingOutcomeIds`]}>
                  {(control) => (
                    <Input {...control} value={line.passingOutcomeIds} invalid={Boolean(errors[`${i}.passingOutcomeIds`])} onChange={(event) => update(i, { passingOutcomeIds: event.target.value })} />
                  )}
                </FormField>
              )}
              <FormField id={`test-${i}-required`} label="Required">
                {(control) => <Input {...control} type="checkbox" checked={line.required} onChange={(event) => update(i, { required: event.target.checked })} />}
              </FormField>
              <FormField id={`test-${i}-destructive`} label="Destructive">
                {(control) => <Input {...control} type="checkbox" checked={line.destructive} onChange={(event) => update(i, { destructive: event.target.checked })} />}
              </FormField>
              {lines.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                  {`Remove test ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the test group" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button onClick={() => setLines([...lines, blankLine])}>Add test</Button>
            <Button type="submit" variant="primary" loading={busy}>
              Save test group
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/test-groups">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
