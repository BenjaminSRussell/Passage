import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getJobPermits, listPermitTypes, newKey, setJobPermits, upsertPermitType, type PermitType, type Problem } from "./api/client";
import { CODE } from "./format";
import { Failure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface TypeValues {
  code: string;
  name: string;
  approverRole: string;
  maxValidityHours: string;
  requiresIsolation: boolean;
  requiresGasTest: boolean;
}

const blank: TypeValues = { code: "", name: "", approverRole: "", maxValidityHours: "12", requiresIsolation: false, requiresGasTest: false };

/** The checks the service makes on a permit type (FR-44.19, 8.44.13), and the type the form sends. */
export function validatePermitType(values: TypeValues): { errors: Partial<Record<keyof TypeValues, string>>; input?: PermitType } {
  const errors: Partial<Record<keyof TypeValues, string>> = {};
  const code = values.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 2 to 20 capital letters, digits or underscores, for example HOT_WORK.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Name the permit type in up to 120 characters.";
  const approverRole = values.approverRole.trim();
  if (approverRole.length < 1 || approverRole.length > 60) errors.approverRole = "Name who approves it, in up to 60 characters.";
  const hours = Number(values.maxValidityHours);
  if (!/^\d{1,3}$/.test(values.maxValidityHours.trim()) || hours < 1 || hours > 168) errors.maxValidityHours = "Enter 1 to 168 hours.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { code, name, approverRole, maxValidityHours: hours, requiresIsolation: values.requiresIsolation, requiresGasTest: values.requiresGasTest },
  };
}

/** Permit setup (eam.setup.admin): permit types, and the permit types each job type requires before work starts (BR-44.11). */
export function PermitTypesPage() {
  const [round, setRound] = useState(0);
  const types = useLoad((signal) => listPermitTypes(signal), `permit-types|${round}`);
  const [values, setValues] = useState<TypeValues>(blank);
  const [errors, setErrors] = useState<Partial<Record<keyof TypeValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<TypeValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePermitType(values);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await upsertPermitType(input, key.current);
      key.current = newKey();
      setValues(blank);
      setNotice(`Permit type ${saved.code} saved.`);
      setRound((n) => n + 1);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<PermitType>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "approverRole", header: "Approver" },
    { key: "maxValidityHours", header: "Max validity", align: "end", render: (row) => `${row.maxValidityHours} h` },
    { key: "requiresIsolation", header: "Isolation (LOTO)", render: (row) => (row.requiresIsolation ? "Required" : "No") },
    { key: "requiresGasTest", header: "Gas test", render: (row) => (row.requiresGasTest ? "Required" : "No") },
    {
      key: "edit",
      header: "Action",
      render: (row) => (
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            setValues({ ...row, maxValidityHours: String(row.maxValidityHours) });
            setErrors({});
            key.current = newKey();
          }}
        >
          Edit
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Permit types" description="Define the permits work may need, how long each stays valid, and which job types require them." />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Permit types">
        <div className={styles.stack}>
          {types.error ? (
            <Failure title="Could not load the permit types" problem={types.error} retry={types.reload} />
          ) : (
            <DataTable
              label="Permit types"
              columns={columns}
              rows={types.data ?? []}
              getRowKey={(row) => row.code}
              loading={types.loading}
              empty={{ title: "No permit types yet", description: "Add HOT_WORK, CONFINED_SPACE, ELECTRICAL_ISOLATION or your own below." }}
            />
          )}
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Save permit type">
            <FormField id="pt-code" label="Code" hint="Saving an existing code replaces that type." error={errors.code}>
              {(control) => <Input {...control} value={values.code} maxLength={20} invalid={Boolean(errors.code)} onChange={(e) => update({ code: e.target.value })} />}
            </FormField>
            <FormField id="pt-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} value={values.name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} />}
            </FormField>
            <FormField id="pt-approver" label="Approver role" hint="For example Area authority or Safety officer." error={errors.approverRole}>
              {(control) => <Input {...control} value={values.approverRole} maxLength={60} invalid={Boolean(errors.approverRole)} onChange={(e) => update({ approverRole: e.target.value })} />}
            </FormField>
            <FormField id="pt-hours" label="Maximum validity (hours)" hint="Issued permits are suspended when they run out." error={errors.maxValidityHours}>
              {(control) => (
                <Input {...control} inputMode="numeric" value={values.maxValidityHours} invalid={Boolean(errors.maxValidityHours)} onChange={(e) => update({ maxValidityHours: e.target.value })} />
              )}
            </FormField>
            <div className={styles.check}>
              <Input id="pt-isolation" type="checkbox" checked={values.requiresIsolation} onChange={(e) => update({ requiresIsolation: e.target.checked })} />
              <label htmlFor="pt-isolation">Requires isolation: every isolation point locked and verified before issue</label>
            </div>
            <div className={styles.check}>
              <Input id="pt-gas" type="checkbox" checked={values.requiresGasTest} onChange={(e) => update({ requiresGasTest: e.target.checked })} />
              <label htmlFor="pt-gas">Requires a gas test</label>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title="Could not save the permit type" problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Save permit type
              </Button>
              {values !== blank ? (
                <Button
                  variant="ghost"
                  onClick={() => {
                    setValues(blank);
                    setErrors({});
                  }}
                >
                  Clear
                </Button>
              ) : null}
            </div>
          </form>
        </div>
      </Card>
      <JobPermitsCard types={types.data} />
    </div>
  );
}

/** The permit types a job type requires (BR-44.11): pick a job type, tick the types, save the whole set. */
function JobPermitsCard({ types }: { types: PermitType[] | null }) {
  const [jobType, setJobType] = useState("");
  const [loaded, setLoaded] = useState<string | null>(null);
  const [chosen, setChosen] = useState<string[]>([]);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"load" | "save" | null>(null);

  async function load(event: FormEvent) {
    event.preventDefault();
    const code = jobType.trim().toUpperCase();
    setFailure(null);
    setNotice(null);
    if (!CODE.test(code)) {
      setError("Use the job type code, for example REPAIR.");
      return;
    }
    setError(undefined);
    setBusy("load");
    try {
      const out = await getJobPermits(code);
      setLoaded(out.jobType);
      setChosen(out.permitTypeCodes ?? []);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
    }
  }

  async function save() {
    if (!loaded) return;
    setFailure(null);
    setNotice(null);
    setBusy("save");
    try {
      const out = await setJobPermits(loaded, chosen);
      setChosen(out.permitTypeCodes ?? []);
      setNotice(
        out.permitTypeCodes.length === 0
          ? `Job type ${out.jobType} needs no permit.`
          : `Job type ${out.jobType} now needs ${out.permitTypeCodes.join(", ")} before work starts.`,
      );
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title="Permits required by job type">
      <div className={styles.stack}>
        <p className={styles.note}>A work order with this job type cannot move to In progress until each required permit is issued and valid.</p>
        <form className={styles.toolbar} onSubmit={load} noValidate aria-label="Choose job type">
          <FormField id="jp-job" label="Job type" error={error}>
            {(control) => (
              <Input
                {...control}
                value={jobType}
                maxLength={20}
                invalid={Boolean(error)}
                onChange={(e) => {
                  setJobType(e.target.value);
                  setLoaded(null);
                }}
              />
            )}
          </FormField>
          <Button type="submit" loading={busy === "load"}>
            Show requirements
          </Button>
        </form>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {failure ? <Failure title="Could not load or save the requirement" problem={failure} /> : null}
        {loaded === null ? null : types === null ? (
          <Skeleton height="control" />
        ) : types.length === 0 ? (
          <p className={styles.note}>Add a permit type above first.</p>
        ) : (
          <fieldset className={styles.lineCard}>
            <legend className={styles.lineTitle}>Required for {loaded}</legend>
            {types.map((t) => (
              <div key={t.code} className={styles.check}>
                <Input
                  id={`jp-${t.code}`}
                  type="checkbox"
                  checked={chosen.includes(t.code)}
                  onChange={(e) => setChosen(e.target.checked ? [...chosen, t.code] : chosen.filter((c) => c !== t.code))}
                />
                <label htmlFor={`jp-${t.code}`}>
                  {t.name} ({t.code})
                </label>
              </div>
            ))}
            <div className={styles.formActions}>
              <Button variant="primary" loading={busy === "save"} onClick={save}>
                Save requirement
              </Button>
            </div>
          </fieldset>
        )}
      </div>
    </Card>
  );
}
