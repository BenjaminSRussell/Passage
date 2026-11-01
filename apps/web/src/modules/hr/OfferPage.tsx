import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, PageHeader } from "../../shell/ui";
import { getOffer, hireFromOffer, moveOffer, type HireFromOffer, type OfferMove, type OfferStatus } from "./api/recruitment";
import { UUID, dayOf, employmentTypeLabel, validFte } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Entry, Facts, FormActions, Loaded, Money, opt, SelectField, Status, TextField, useAction, useSubmit } from "./parts";
import { useLoad } from "./useLoad";

const MOVES: Record<string, Array<{ to: OfferMove; label: string; done: string }>> = {
  DRAFT: [{ to: "SENT", label: "Send offer", done: "The offer was sent." }],
  SENT: [
    { to: "ACCEPTED", label: "Record acceptance", done: "The offer was accepted." },
    { to: "DECLINED", label: "Record decline", done: "The offer was declined." },
  ],
};
const WITHDRAWABLE: OfferStatus[] = ["DRAFT", "SENT", "ACCEPTED"];

export interface HireValues {
  workerId: string;
  legalEntityId: string;
  employmentType: string;
  fteShare: string;
  compPlanId: string;
}

export function validateHire(v: HireValues): { errors: Record<string, string>; input?: HireFromOffer } {
  const errors: Record<string, string> = {};
  if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the new worker's id.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id.";
  if (v.fteShare.trim() !== "" && !validFte(v.fteShare, 1)) errors.fteShare = "Use a decimal above 0 and up to 2.00, with at most two decimals.";
  if (v.compPlanId.trim() !== "" && !UUID.test(v.compPlanId.trim())) errors.compPlanId = "Enter the compensation plan's id.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      workerId: v.workerId.trim(),
      legalEntityId: v.legalEntityId.trim(),
      employmentType: v.employmentType,
      fteShare: opt(v.fteShare),
      compPlanId: opt(v.compPlanId),
    },
  };
}

function HireForm({ offerId, onConverted }: { offerId: string; onConverted(): void }) {
  const navigate = useNavigate();
  const [values, setValues] = useState<HireValues>({ workerId: "", legalEntityId: "", employmentType: "FULL_TIME", fteShare: "", compPlanId: "" });
  const form = useSubmit({ "HCM-OFR-003": "legalEntityId", "HCM-OFR-002": "compPlanId" });
  const set = (patch: Partial<HireValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateHire(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run(
      () => hireFromOffer(offerId, input),
      (action) => {
        onConverted();
        navigate(`/hr/actions/${action.id}`);
      },
    );
  }

  const text = (name: Exclude<keyof HireValues, "employmentType">, label: string, optional = false, hint?: string) => (
    <TextField id={`hire-${name}`} label={label} optional={optional} hint={hint} value={values[name]} error={form.errors[name]} onChange={(v) => set({ [name]: v })} />
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Hire from this offer">
      {text("workerId", "Worker id", false, "The worker record created for this person.")}
      {text("legalEntityId", "Legal entity id")}
      <SelectField
        id="hire-employmentType"
        label="Employment type"
        value={values.employmentType}
        error={form.errors.employmentType}
        onChange={(v) => set({ employmentType: v })}
        options={Object.entries(employmentTypeLabel)}
      />
      {text("fteShare", "FTE share", true)}
      {text("compPlanId", "Compensation plan id", true, "Setting pay needs the compensation administrator duty.")}
      <FormActions busy={form.busy} submit="Hire from offer" failure={form.failure} failureTitle="Could not hire from the offer" />
    </form>
  );
}

export function OfferPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getOffer(id, signal), id);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Offer" what="offer" backTo="/hr/requisitions">
      {(offer) => (
        <div className={styles.stack}>
          <PageHeader title="Offer" meta={<Status value={offer.status} />} actions={<BackLink to={`/hr/candidates/${offer.candidateId}`}>Back to candidate</BackLink>} />
          {action.feedback}
          <Card
            title="Offer"
            actions={
              <div className={styles.rowActions}>
                {(MOVES[offer.status] ?? []).map((move) => (
                  <Button key={move.to} variant="primary" loading={action.busy === move.to} onClick={() => void action.run(move.to, () => moveOffer(offer.id, move.to), move.done)}>
                    {move.label}
                  </Button>
                ))}
                {WITHDRAWABLE.includes(offer.status) ? (
                  <ConfirmAction
                    label="Withdraw offer"
                    title="Withdraw this offer?"
                    description="The candidate will no longer be able to accept it."
                    confirmLabel="Withdraw"
                    busy={action.busy === "WITHDRAWN"}
                    onConfirm={() => void action.run("WITHDRAWN", () => moveOffer(offer.id, "WITHDRAWN"), "The offer was withdrawn.")}
                  />
                ) : null}
              </div>
            }
          >
            <Facts>
              <Entry term="Amount">
                <Money amount={offer.amount} currency={offer.currency} />
              </Entry>
              <Entry term="Start date" mono>
                {dayOf(offer.startDate)}
              </Entry>
              <Entry term="Candidate" mono>
                <Link to={`/hr/candidates/${offer.candidateId}`}>{offer.candidateId}</Link>
              </Entry>
              <Entry term="Requisition" mono>
                <Link to={`/hr/requisitions/${offer.requisitionId}`}>{offer.requisitionId}</Link>
              </Entry>
              {offer.hireActionId ? (
                <Entry term="Hire action" mono>
                  <Link to={`/hr/actions/${offer.hireActionId}`}>{offer.hireActionId}</Link>
                </Entry>
              ) : null}
            </Facts>
          </Card>
          {offer.status === "ACCEPTED" ? (
            offer.hireActionId ? (
              <Alert tone="info">This offer has already been converted to a hire.</Alert>
            ) : (
              <Card title="Hire">
                <HireForm offerId={offer.id} onConverted={state.reload} />
              </Card>
            )
          ) : null}
        </div>
      )}
    </Loaded>
  );
}
