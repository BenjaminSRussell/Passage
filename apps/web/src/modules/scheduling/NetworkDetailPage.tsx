import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getNetwork, listResources, newKey, updateNetwork, type Network, type NetworkEdge, type NetworkPatch, type Problem } from "./api/client";
import { dash, formatMinutes, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Partial<Record<"name" | "activeTo" | "form", string>>;

/** Checks that mirror the service, and the patch the form sends: only what changed. */
export function validateNetworkEdit(values: { name: string; activeTo: string }, current: Network): { errors: Errors; patch?: NetworkPatch } {
  const errors: Errors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const activeTo = values.activeTo.trim();
  if (activeTo !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(activeTo)) errors.activeTo = "Enter a date (YYYY-MM-DD).";
  else if (activeTo !== "" && activeTo < current.activeFrom) errors.activeTo = `The end date cannot be before the start date ${current.activeFrom}.`;
  else if (activeTo === "" && current.activeTo !== null) errors.activeTo = "An end date can be moved but not cleared; enter a later date.";
  if (Object.keys(errors).length > 0) return { errors };
  const patch: NetworkPatch = {};
  if (name !== current.name) patch.name = name;
  if (activeTo !== "" && activeTo !== current.activeTo) patch.activeTo = activeTo;
  if (Object.keys(patch).length === 0) {
    errors.form = "Nothing changed.";
    return { errors };
  }
  return { errors, patch };
}

/**
 * The form is keyed on the network version, so loading the latest version
 * (after a stale save or after this form's own save) starts it from the
 * stored values. onSaved(null) clears the parent's saved message.
 */
function EditForm({ initial, onReload, onSaved }: { initial: Network; onReload: () => void; onSaved: (version: number | null) => void }) {
  const [network, setNetwork] = useState<Network>(initial);
  const [values, setValues] = useState({ name: initial.name, activeTo: initial.activeTo ?? "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<typeof values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, patch } = validateNetworkEdit(values, network);
    setErrors(found);
    setFailure(null);
    onSaved(null);
    if (!patch) return;
    setBusy(true);
    try {
      const next = await updateNetwork(network.id, network.version, patch, key.current);
      setNetwork(next);
      key.current = newKey();
      onSaved(next.version);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "activeTo"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <FormField id="network-name" label="Name" error={errors.name}>
        {(control) => <Input {...control} value={values.name} maxLength={120} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
      </FormField>
      <FormField
        id="network-end"
        label="Active until"
        hint={`Last day the network applies, on or after ${network.activeFrom}. Runs and moves after that day ignore it; jobs already placed stay where they are.`}
        error={errors.activeTo}
      >
        {(control) => <Input {...control} type="date" value={values.activeTo} onChange={(event) => update({ activeTo: event.target.value })} invalid={Boolean(errors.activeTo)} />}
      </FormField>
      {errors.form ? (
        <p role="alert" className={styles.fieldError}>
          {errors.form}
        </p>
      ) : null}
      {failure ? (
        <div className={styles.formActions}>
          {failure.code === "SCHED-0128" ? (
            <Alert
              tone="danger"
              title="The network changed since you opened it"
              live="alert"
              action={
                <Button size="sm" onClick={onReload}>
                  Load the latest version
                </Button>
              }
            >
              <ProblemMessage problem={failure} /> Loading the latest version discards your changes on this screen.
            </Alert>
          ) : (
            <Alert tone="danger" title="Could not save the network" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          )}
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Save changes
        </Button>
      </div>
    </form>
  );
}

interface Detail {
  network: Network;
  codes: Record<string, string>;
}

/** One resource network with its allowed transitions. */
export function NetworkDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Detail>(async (signal) => {
    const [network, resources] = await Promise.all([getNetwork(id, signal), listResources(signal)]);
    return { network, codes: Object.fromEntries(resources.map((r) => [r.id, `${r.name} (${r.code})`])) };
  }, id);
  const [saved, setSaved] = useState<number | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Resource network" />
        <Alert tone="danger" title="Could not load the network" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Resource network" />
        <span className="visually-hidden">Loading the network…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { network, codes } = state.data;
  const code = (rid?: string) => (rid ? (codes[rid] ?? shortId(rid)) : dash);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={network.code}
        description={`${network.name}. Active from ${network.activeFrom}${network.activeTo ? ` to ${network.activeTo}` : ""}.`}
        actions={
          <Link className={buttonClassName("ghost")} to="/scheduling/networks">
            All networks
          </Link>
        }
      />
      <Card title="Rename or end the network">
        <EditForm
          key={`${network.id}:${network.version}`}
          initial={network}
          onReload={state.reload}
          onSaved={(version) => {
            setSaved(version);
            if (version !== null) state.reload();
          }}
        />
        <Alert tone="success" live="status">
          {saved !== null ? `The network was saved (version ${saved}).` : ""}
        </Alert>
      </Card>
      <Card title="Allowed transitions">
        <DataTable
          label="Allowed transitions"
          columns={[
            { key: "from", header: "From", mono: true, render: (e: NetworkEdge) => code(e.fromResourceId) },
            { key: "to", header: "To", mono: true, render: (e: NetworkEdge) => code(e.toResourceId) },
            { key: "transfer", header: "Transfer", align: "end", render: (e: NetworkEdge) => formatMinutes(e.transferMinutes) },
            { key: "wait", header: "Maximum wait", align: "end", render: (e: NetworkEdge) => (e.maxWaitMinutes === null ? "No limit" : formatMinutes(e.maxWaitMinutes)) },
            { key: "secondary", header: "Transfer resource", mono: true, render: (e: NetworkEdge) => code(e.secondaryResourceId) },
          ]}
          rows={network.edges}
          getRowKey={(e) => `${e.fromResourceId}>${e.toResourceId}`}
          empty={{ title: "No transitions", description: "This network allows no resource changes." }}
        />
      </Card>
    </div>
  );
}
