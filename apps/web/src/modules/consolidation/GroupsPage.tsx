import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { createGroup, listGroups, newKey, type Group } from "./api/groups";
import { isCurrency } from "./format";
import styles from "./pages.module.css";

const columns: DataTableColumn<Group>[] = [
  { key: "code", header: "Group", mono: true, render: (g) => g.code },
  { key: "name", header: "Name", render: (g) => g.name },
  { key: "currency", header: "Group currency", mono: true, render: (g) => g.currency },
  { key: "units", header: "Units", align: "end", render: (g) => String(g.unitCount ?? 0) },
];

/** Check a new group's fields the way the service does. */
export function validateGroup(values: { code: string; name: string; currency: string }): { errors: Record<string, string>; request?: { code: string; name: string; currency: string } } {
  const errors: Record<string, string> = {};
  const code = values.code.trim();
  const name = values.name.trim();
  const currency = values.currency.trim().toUpperCase();
  if (!/^[A-Za-z0-9._:-]{1,64}$/.test(code)) errors.code = "Enter a short code of letters, digits and . _ : -.";
  if (name === "" || name.length > 200) errors.name = "Enter a name of up to 200 characters.";
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  return Object.keys(errors).length > 0 ? { errors } : { errors, request: { code, name, currency } };
}

/** The consolidation groups of the tenant, and a form to add one (FR-40.7). */
export function GroupsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<{ loading: boolean; items: Group[]; failure: Problem | null }>({ loading: true, items: [], failure: null });
  const [reload, setReload] = useState(0);
  const [values, setValues] = useState({ code: "", name: "", currency: "USD" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, failure: null }));
    listGroups(ctl.signal)
      .then((page) => setState({ loading: false, items: page.items, failure: null }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setState({ loading: false, items: [], failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [reload]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateGroup(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      const group = await createGroup(request, key.current);
      key.current = newKey();
      navigate(`/consolidation/groups/${group.id}`);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consolidation groups"
        description="A group is the set of legal entities that are consolidated together, with the currency its statements are in. Add the entities, their ownership and the account mapping on the group's page."
      />
      {state.failure ? (
        <Alert tone="danger" title="We couldn't load the groups" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
          <ProblemMessage problem={state.failure} />
        </Alert>
      ) : (
        <DataTable
          label="Consolidation groups"
          columns={columns}
          rows={state.items}
          getRowKey={(g) => g.id}
          loading={state.loading}
          onRowOpen={(g) => navigate(`/consolidation/groups/${g.id}`)}
          empty={{ title: "No groups yet", description: "Add the first group below, then add its legal entities." }}
        />
      )}
      <Card title="New group" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="group-code" label="Code" error={errors.code} hint="Letters, digits and . _ : -.">
            {(c) => <Input {...c} className={styles.mono} value={values.code} onChange={(e) => setValues({ ...values, code: e.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="group-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={values.name} onChange={(e) => setValues({ ...values, name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="group-currency" label="Group currency" error={errors.currency}>
            {(c) => <Input {...c} className={styles.mono} value={values.currency} onChange={(e) => setValues({ ...values, currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The group was not created" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create group
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
