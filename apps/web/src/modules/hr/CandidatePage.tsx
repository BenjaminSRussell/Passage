import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, PageHeader } from "../../shell/ui";
import { createOffer, getCandidate, moveCandidate, type CandidateMove, type CandidateStage, type NewOffer } from "./api/recruitment";
import { DATE, dash, formatUtc, validRate } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Entry, Facts, FormActions, Loaded, Status, TextField, useAction, useSubmit } from "./parts";
import { CandidateName } from "./RequisitionDetailPage";
import { useLoad } from "./useLoad";

const NEXT: Partial<Record<CandidateStage, CandidateMove>> = { APPLIED: "SCREEN", SCREEN: "INTERVIEW", INTERVIEW: "OFFER" };
const NEXT_LABEL: Record<string, string> = { SCREEN: "Move to screening", INTERVIEW: "Move to interview", OFFER: "Move to offer" };

export function validateOffer(v: { amount: string; currency: string; startDate: string }): { errors: Record<string, string>; input?: NewOffer } {
  const errors: Record<string, string> = {};
  const currency = v.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) errors.currency = "Use a three-letter currency code, for example USD.";
  else if (!validRate(v.amount, currency)) errors.amount = "Enter an amount within the currency's decimals.";
  if (!DATE.test(v.startDate)) errors.startDate = "Choose the start date.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { amount: v.amount.trim(), currency, startDate: v.startDate } };
}

function OfferForm({ candidateId }: { candidateId: string }) {
  const navigate = useNavigate();
  const [values, setValues] = useState({ amount: "", currency: "", startDate: "" });
  const form = useSubmit({ "HCM-OFR-002": "amount" });
  const set = (patch: Partial<typeof values>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateOffer(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createOffer(candidateId, input, key), (offer) => navigate(`/hr/offers/${offer.id}`));
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Create an offer">
      <TextField id="offer-amount" label="Amount" value={values.amount} error={form.errors.amount} onChange={(v) => set({ amount: v })} />
      <TextField id="offer-currency" label="Currency" maxLength={3} value={values.currency} error={form.errors.currency} onChange={(v) => set({ currency: v })} />
      <TextField id="offer-startDate" label="Start date" type="date" value={values.startDate} error={form.errors.startDate} onChange={(v) => set({ startDate: v })} />
      <FormActions errors={form.errors} busy={form.busy} submit="Create offer" failure={form.failure} failureTitle="Could not create the offer" />
    </form>
  );
}

export function CandidatePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCandidate(id, signal), id);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Candidate" what="candidate" backTo="/hr/requisitions">
      {(candidate) => {
        const next = NEXT[candidate.stage];
        const final = candidate.stage === "HIRED" || candidate.stage === "REJECTED";
        return (
          <div className={styles.stack}>
            <PageHeader
              title={candidate.erased ? "Erased candidate" : candidate.fullName ?? "Candidate"}
              meta={<Status value={candidate.stage} />}
              actions={<BackLink to={`/hr/requisitions/${candidate.requisitionId}`}>Back to requisition</BackLink>}
            />
            {action.feedback}
            {candidate.erased ? (
              <Alert tone="info" title="Personal data erased">
                The retention policy removed this candidate's name and contact details. Only the pipeline record remains.
              </Alert>
            ) : null}
            <Card
              title="Candidate"
              actions={
                final ? null : (
                  <div className={styles.rowActions}>
                    {next ? (
                      <Button variant="primary" loading={action.busy === next} onClick={() => void action.run(next, () => moveCandidate(candidate.id, next), `Moved to ${next.toLowerCase()}.`)}>
                        {NEXT_LABEL[next]}
                      </Button>
                    ) : null}
                    <ConfirmAction
                      label="Reject candidate"
                      title="Reject this candidate?"
                      description="A rejected candidate can't move back into the pipeline."
                      confirmLabel="Reject"
                      busy={action.busy === "REJECTED"}
                      onConfirm={() => void action.run("REJECTED", () => moveCandidate(candidate.id, "REJECTED"), "The candidate was rejected.")}
                    />
                  </div>
                )
              }
            >
              <Facts>
                <Entry term="Name">
                  <CandidateName candidate={candidate} />
                </Entry>
                {candidate.email ? <Entry term="Email">{candidate.email}</Entry> : null}
                {candidate.phone ? <Entry term="Phone">{candidate.phone}</Entry> : null}
                <Entry term="Source">{candidate.source ?? dash}</Entry>
                <Entry term="In stage since" mono>
                  {formatUtc(candidate.stageChangedAt)}
                </Entry>
                <Entry term="Requisition" mono>
                  <Link to={`/hr/requisitions/${candidate.requisitionId}`}>{candidate.requisitionId}</Link>
                </Entry>
              </Facts>
              <p className={styles.note}>This view is recorded in the audit log. Only details the service returned are shown.</p>
            </Card>
            {candidate.stage === "OFFER" ? (
              <Card title="Offer">
                <OfferForm candidateId={candidate.id} />
              </Card>
            ) : null}
          </div>
        );
      }}
    </Loaded>
  );
}
