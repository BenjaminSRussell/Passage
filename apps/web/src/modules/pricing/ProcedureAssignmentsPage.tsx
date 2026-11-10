import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, ConfirmDialog, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  clearProcedureAssignment,
  describeProblem,
  listProcedureAssignments,
  listProcedures,
  newKey,
  PricingApiError,
  setProcedureAssignment,
  type Problem,
  type ProcedureAssignment,
} from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const DUTY_ADMIN = "pricing.admin";
const CHANNEL = /^([A-Z0-9][A-Z0-9_-]{0,31})?$/;

const channelLabel = (a: ProcedureAssignment) => (a.salesChannel === "" ? "All channels (default)" : a.salesChannel);

/** Procedure determination (PRD 5.26.13): which pricing procedure each sales channel of a legal entity prices with. */
export function ProcedureAssignmentsPage() {
  const canChange = holdsDuty(usePrincipalDuties(), DUTY_ADMIN);
  const [legalEntityId, setLegalEntityId] = useState("");
  const [idError, setIdError] = useState("");
  const [loaded, setLoaded] = useState<{ legalEntityId: string; rows: ProcedureAssignment[]; names: string[] } | null>(null);
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [channel, setChannel] = useState("");
  const [name, setName] = useState("");
  const [channelError, setChannelError] = useState("");
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<ProcedureAssignment | null>(null);
  // One Idempotency-Key per command: a retry of the same save or removal reuses it, any other command gets a fresh one.
  const key = useRef({ command: "", value: newKey() });

  async function reload(le: string) {
    const [rows, procedures] = await Promise.all([listProcedureAssignments(le), listProcedures(le)]);
    const names = [...new Set(procedures.filter((p) => p.status === "ACTIVE").map((p) => p.name))].sort();
    setLoaded({ legalEntityId: le, rows, names });
    setName((current) => (names.includes(current) ? current : (names[0] ?? "")));
  }

  async function load(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved("");
    if (!UUID.test(legalEntityId.trim())) {
      setIdError("Enter the legal entity id (a UUID).");
      return;
    }
    setIdError("");
    setLoading(true);
    try {
      await reload(legalEntityId.trim());
    } catch (error) {
      setLoaded(null);
      setFailure(describeProblem(error));
    } finally {
      setLoading(false);
    }
  }

  async function run(command: string, action: (idempotencyKey: string) => Promise<void>, done: string) {
    if (!loaded) return;
    if (key.current.command !== command) key.current = { command, value: newKey() };
    setBusy(true);
    setFailure(null);
    setSaved("");
    try {
      await action(key.current.value);
      key.current = { command: "", value: newKey() };
      await reload(loaded.legalEntityId);
      setSaved(done);
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = { command: "", value: newKey() };
    } finally {
      setBusy(false);
    }
  }

  function save(event: FormEvent) {
    event.preventDefault();
    if (!loaded) return;
    if (!CHANNEL.test(channel)) {
      setChannelError("Use up to 32 upper-case letters, digits, '_' or '-', such as WEB. Leave it empty for the default.");
      return;
    }
    setChannelError("");
    const existing = loaded.rows.find((r) => r.salesChannel === channel);
    const version = existing?.version ?? 0;
    void run(
      JSON.stringify(["set", loaded.legalEntityId, channel, name, version]),
      async (k) => void (await setProcedureAssignment({ legalEntityId: loaded.legalEntityId, salesChannel: channel, procedureName: name, version }, k)),
      `${channel === "" ? "The default" : channel} now prices with ${name}.`,
    );
  }

  const columns: DataTableColumn<ProcedureAssignment>[] = [
    { key: "salesChannel", header: "Sales channel", render: channelLabel },
    { key: "procedureName", header: "Procedure", mono: true },
    { key: "version", header: "Version", align: "end" },
    { key: "updatedBy", header: "Changed by", render: (a) => a.updatedBy || "—" },
    ...(canChange
      ? [
          {
            key: "actions",
            header: "Actions",
            render: (a: ProcedureAssignment) => (
              <Button variant="ghost" size="sm" onClick={() => setRemoving(a)}>
                {`Remove ${channelLabel(a)}`}
              </Button>
            ),
          } satisfies DataTableColumn<ProcedureAssignment>,
        ]
      : []),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Procedure determination"
        description="Choose the pricing procedure each sales channel prices with. A channel without its own row uses the default; with neither, the reference order applies."
      />
      <Card>
        <form className={styles.form} onSubmit={load} noValidate>
          <FormField id="pd-legal-entity" label="Legal entity" error={idError}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={Boolean(idError)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="secondary" loading={loading}>
              Load assignments
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="Could not complete the request" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {saved ? (
        <Alert tone="success" title="Assignment saved" live="status">
          {saved}
        </Alert>
      ) : null}
      {loading ? <Skeleton height="tile" /> : null}
      {!loading && !loaded && !failure ? <EmptyState title="No legal entity loaded" description="Enter a legal entity id to see which procedures its channels use." /> : null}
      {!loading && loaded ? (
        <>
          <Card title="Assignments">
            <DataTable
              label="Procedure assignments"
              columns={columns}
              rows={loaded.rows}
              getRowKey={(a) => a.salesChannel || "default"}
              empty={{ title: "Nothing assigned", description: "This legal entity prices in the reference order until a procedure is assigned." }}
            />
          </Card>
          {canChange ? (
            <Card title="Assign a procedure">
              {loaded.names.length === 0 ? (
                <EmptyState title="No active procedure" description="Activate a pricing procedure first; only an active procedure can be assigned." headingLevel={3} />
              ) : (
                <form className={styles.form} onSubmit={save} noValidate>
                  <FormField id="pd-channel" label="Sales channel" optional hint="Leave empty for the legal entity's default." error={channelError}>
                    {(c) => <Input {...c} className={styles.mono} value={channel} onChange={(e) => setChannel(e.target.value)} invalid={Boolean(channelError)} />}
                  </FormField>
                  <FormField id="pd-procedure" label="Procedure" hint="The active version of this procedure is used.">
                    {(c) => (
                      <Select {...c} value={name} onChange={(e) => setName(e.target.value)}>
                        {loaded.names.map((n) => (
                          <option key={n} value={n}>
                            {n}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <div className={styles.formActions}>
                    <Button type="submit" variant="primary" loading={busy}>
                      Save assignment
                    </Button>
                  </div>
                </form>
              )}
            </Card>
          ) : (
            <p className={styles.note}>Changing assignments needs the {DUTY_ADMIN} duty.</p>
          )}
        </>
      ) : null}
      {removing && loaded ? (
        <ConfirmDialog
          title="Remove this assignment?"
          description={`${channelLabel(removing)} will no longer price with ${removing.procedureName}. It falls back to the default assignment, or to the reference order when there is none.`}
          confirmLabel="Remove assignment"
          cancelLabel="Keep it"
          onConfirm={() => {
            const target = removing;
            setRemoving(null);
            void run(
              JSON.stringify(["clear", target.legalEntityId, target.salesChannel, target.version]),
              (k) => clearProcedureAssignment(target.legalEntityId, target.salesChannel, target.version, k),
              `${channelLabel(target)} was removed.`,
            );
          }}
          onCancel={() => setRemoving(null)}
        />
      ) : null}
    </div>
  );
}
