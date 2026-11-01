import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  createCandidate,
  getRequisition,
  listCandidates,
  moveRequisition,
  type Candidate,
  type CandidateStage,
  type NewCandidate,
  type RequisitionMove,
  type RequisitionStatus,
} from "./api/recruitment";
import { dash, dayOf, formatUtc } from "./format";
import styles from "./pages.module.css";
import { BackLink, CheckField, ConfirmAction, Entry, Facts, FormActions, labelled, Loaded, opt, PagedTable, SelectField, Status, TextField, useAction, useSubmit } from "./parts";
import { useLoad } from "./useLoad";

const STAGES: CandidateStage[] = ["APPLIED", "SCREEN", "INTERVIEW", "OFFER", "HIRED", "REJECTED"];
const NEXT: Partial<Record<RequisitionStatus, RequisitionMove>> = { DRAFT: "APPROVED", APPROVED: "OPEN" };
const NEXT_LABEL: Record<string, string> = { APPROVED: "Approve", OPEN: "Open for candidates" };

export function CandidateName({ candidate }: { candidate: Candidate }) {
  if (candidate.erased) return <>Erased by the retention purge</>;
  return <>{candidate.fullName ?? "Name not shown"}</>;
}

const columns: DataTableColumn<Candidate>[] = [
  { key: "fullName", header: "Candidate", render: (row) => <Link to={`/hr/candidates/${row.id}`}><CandidateName candidate={row} /></Link> },
  { key: "stage", header: "Stage", render: (row) => <Status value={row.stage} /> },
  { key: "source", header: "Source", render: (row) => row.source ?? dash },
  { key: "stageChangedAt", header: "In stage since", mono: true, render: (row) => formatUtc(row.stageChangedAt) },
];

export interface CandidateValues {
  fullName: string;
  email: string;
  phone: string;
  source: string;
  consentGranted: boolean;
}

export function validateCandidate(v: CandidateValues): { errors: Record<string, string>; input?: NewCandidate } {
  const errors: Record<string, string> = {};
  const fullName = v.fullName.trim();
  if (fullName === "" || fullName.length > 120) errors.fullName = "Enter a name of up to 120 characters.";
  const email = v.email.trim();
  if (email !== "" && (email.length > 200 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))) errors.email = "Enter an email address, or leave it empty.";
  if (v.phone.trim().length > 40) errors.phone = "Use up to 40 characters.";
  if (v.source.trim().length > 60) errors.source = "Use up to 60 characters.";
  if (!v.consentGranted) errors.consentGranted = "Record the candidate's consent before adding them.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { fullName, email: opt(email), phone: opt(v.phone), source: opt(v.source), consentGranted: true } };
}

function AddCandidate({ requisitionId, onAdded, onCancel }: { requisitionId: string; onAdded(candidate: Candidate): void; onCancel(): void }) {
  const [values, setValues] = useState<CandidateValues>({ fullName: "", email: "", phone: "", source: "", consentGranted: false });
  const form = useSubmit({ "HCM-CNS-001": "consentGranted" });
  const set = (patch: Partial<CandidateValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateCandidate(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createCandidate(requisitionId, input, key), onAdded);
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Add a candidate">
      <TextField id="candidate-fullName" label="Full name" value={values.fullName} error={form.errors.fullName} onChange={(v) => set({ fullName: v })} />
      <TextField id="candidate-email" label="Email" optional type="email" value={values.email} error={form.errors.email} onChange={(v) => set({ email: v })} />
      <TextField id="candidate-phone" label="Phone" optional value={values.phone} error={form.errors.phone} onChange={(v) => set({ phone: v })} />
      <TextField id="candidate-source" label="Source" optional value={values.source} error={form.errors.source} onChange={(v) => set({ source: v })} />
      <div className={styles.wide}>
        <CheckField
          id="candidate-consentGranted"
          label="The candidate consented to us keeping their personal data for this recruitment"
          checked={values.consentGranted}
          error={form.errors.consentGranted}
          onChange={(consentGranted) => set({ consentGranted })}
        />
      </div>
      <FormActions errors={form.errors} busy={form.busy} submit="Add candidate" failure={form.failure} failureTitle="Could not add the candidate" />
      <div className={styles.formActions}>
        <Button onClick={onCancel}>Close form</Button>
      </div>
    </form>
  );
}

export function RequisitionDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRequisition(id, signal), id);
  const [stage, setStage] = useState("");
  const [adding, setAdding] = useState(false);
  const [added, setAdded] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Requisition" what="requisition" backTo="/hr/requisitions">
      {(req) => {
        const next = NEXT[req.status];
        const open = req.status === "OPEN";
        return (
          <div className={styles.stack}>
            <PageHeader
              title={`Requisition ${req.requisitionNumber}`}
              meta={<Status value={req.status} />}
              actions={<BackLink to="/hr/requisitions">Back to requisitions</BackLink>}
            />
            {action.feedback}
            <Card
              title="Requisition"
              actions={
                <div className={styles.rowActions}>
                  {next ? (
                    <Button variant="primary" loading={action.busy === next} onClick={() => void action.run(next, () => moveRequisition(req.id, next), `The requisition is now ${next.toLowerCase()}.`)}>
                      {NEXT_LABEL[next]}
                    </Button>
                  ) : null}
                  {req.status === "DRAFT" || req.status === "APPROVED" || open ? (
                    <ConfirmAction
                      label="Cancel requisition"
                      title="Cancel this requisition?"
                      description="Candidates stay on record, but no one more can be hired against it."
                      confirmLabel="Cancel requisition"
                      busy={action.busy === "CANCELED"}
                      onConfirm={() => void action.run("CANCELED", () => moveRequisition(req.id, "CANCELED"), "The requisition was cancelled.")}
                    />
                  ) : null}
                </div>
              }
            >
              <Facts>
                <Entry term="Position" mono>
                  <Link to={`/hr/positions/${req.positionId}`}>{req.positionId}</Link>
                </Entry>
                <Entry term="Hiring manager" mono>
                  <Link to={`/hr/workers/${req.hiringManagerWorkerId}`}>{req.hiringManagerWorkerId}</Link>
                </Entry>
                <Entry term="Filled" mono>{`${req.filled} of ${req.headcount}`}</Entry>
                <Entry term="Planned start" mono>
                  {dayOf(req.plannedStartDate)}
                </Entry>
                <Entry term="Overfill">{req.allowOverfill ? "Allowed" : "Not allowed"}</Entry>
                <Entry term="Legal entity" mono>
                  {req.legalEntityId}
                </Entry>
              </Facts>
              <p className={styles.note}>Opening checks that the position has room on the planned start date.</p>
            </Card>
            <Card
              title="Candidates"
              actions={
                open && !adding ? (
                  <Button size="sm" onClick={() => setAdding(true)}>
                    Add candidate
                  </Button>
                ) : null
              }
            >
              <div className={styles.stack}>
                <Alert tone="success" live="status">
                  {notice}
                </Alert>
                {!open ? <Alert tone="info">Candidates can be added while the requisition is open.</Alert> : null}
                <p className={styles.note}>Viewing candidates is recorded in the audit log. Contact details appear only when the service returns them.</p>
                {adding ? (
                  <AddCandidate
                    requisitionId={req.id}
                    onCancel={() => setAdding(false)}
                    onAdded={(candidate) => {
                      setAdding(false);
                      setAdded((n) => n + 1);
                      setNotice(`${candidate.fullName ?? "The candidate"} was added.`);
                    }}
                  />
                ) : null}
                <div className={styles.toolbar}>
                  <SelectField id="candidate-stage" label="Stage" value={stage} onChange={setStage} options={labelled(STAGES)} placeholder="All stages" />
                </div>
                <PagedTable<Candidate>
                  label="Candidates"
                  noun="candidates"
                  load={(cursor, signal) => listCandidates(req.id, { stage, cursor }, signal)}
                  loadKey={`candidates|${req.id}|${stage}|${added}`}
                  columns={columns}
                  getRowKey={(row) => row.id}
                  empty={{ title: "No candidates", description: stage ? "No one is at this stage." : "Add candidates once the requisition is open." }}
                />
              </div>
            </Card>
          </div>
        );
      }}
    </Loaded>
  );
}
