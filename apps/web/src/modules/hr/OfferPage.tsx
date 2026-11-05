import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
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

export const DUTY_WORKER_MAINTAIN = "HcmWorkerMaintain";
const PERSONNEL_NUMBER = /^[A-Z0-9][A-Z0-9-]{0,19}$/;

export interface HireValues {
  workerMode?: "existing" | "new";
  workerId: string;
  personnelNumber?: string;
  workerType?: string;
  partyPersonId?: string;
  nationalId?: string;
  legalName?: string;
  legalEntityId: string;
  employmentType: string;
  fteShare: string;
  compPlanId: string;
}

export function validateHire(v: HireValues): { errors: Record<string, string>; input?: HireFromOffer } {
  const errors: Record<string, string> = {};
  const creating = v.workerMode === "new";
  const personnelNumber = (v.personnelNumber ?? "").trim().toUpperCase();
  const nationalId = (v.nationalId ?? "").trim();
  const legalName = (v.legalName ?? "").trim();
  if (creating) {
    if (!PERSONNEL_NUMBER.test(personnelNumber)) errors.personnelNumber = "Use 1 to 20 characters of A-Z, 0-9 or -.";
    if (!UUID.test((v.partyPersonId ?? "").trim())) errors.partyPersonId = "Enter the person's party id.";
    if (nationalId === "" || nationalId.length > 32) errors.nationalId = "Enter the national id, at most 32 characters.";
    if (legalName.length > 200) errors.legalName = "Use at most 200 characters.";
  } else if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the new worker's id.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id.";
  if (v.fteShare.trim() !== "" && !validFte(v.fteShare, 1)) errors.fteShare = "Use a decimal above 0 and up to 2.00, with at most two decimals.";
  if (v.compPlanId.trim() !== "" && !UUID.test(v.compPlanId.trim())) errors.compPlanId = "Enter the compensation plan's id.";
  if (Object.keys(errors).length > 0) return { errors };
  const worker: Pick<HireFromOffer, "workerId" | "newWorker"> = creating
    ? {
        newWorker: {
          personnelNumber,
          workerType: v.workerType ?? "EMPLOYEE",
          partyPersonId: (v.partyPersonId ?? "").trim(),
          nationalId,
          legalName: legalName === "" ? undefined : legalName,
        },
      }
    : { workerId: v.workerId.trim() };
  return {
    errors,
    input: {
      ...worker,
      legalEntityId: v.legalEntityId.trim(),
      employmentType: v.employmentType,
      fteShare: opt(v.fteShare),
      compPlanId: opt(v.compPlanId),
    },
  };
}

function HireForm({ offerId, onConverted }: { offerId: string; onConverted(): void }) {
  const navigate = useNavigate();
  const canCreateWorker = holdsDuty(usePrincipalDuties(), DUTY_WORKER_MAINTAIN);
  const [values, setValues] = useState<HireValues>({
    workerMode: "existing",
    workerId: "",
    personnelNumber: "",
    workerType: "EMPLOYEE",
    partyPersonId: "",
    nationalId: "",
    legalName: "",
    legalEntityId: "",
    employmentType: "FULL_TIME",
    fteShare: "",
    compPlanId: "",
  });
  const form = useSubmit({ "HCM-OFR-003": "legalEntityId", "HCM-OFR-002": "compPlanId", "HCM-OFR-004": "legalName", "HCM-WRK-DUPLICATE": "personnelNumber" });
  const creating = canCreateWorker && values.workerMode === "new";
  const set = (patch: Partial<HireValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateHire({ ...values, workerMode: creating ? "new" : "existing" });
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

  const text = (name: Exclude<keyof HireValues, "employmentType" | "workerMode" | "workerType">, label: string, optional = false, hint?: string) => (
    <TextField id={`hire-${name}`} label={label} optional={optional} hint={hint} value={values[name] ?? ""} error={form.errors[name]} onChange={(v) => set({ [name]: v })} />
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Hire from this offer">
      {canCreateWorker ? (
        <SelectField
          id="hire-workerMode"
          label="Worker record"
          value={values.workerMode ?? "existing"}
          onChange={(v) => set({ workerMode: v === "new" ? "new" : "existing" })}
          options={[
            ["existing", "Use an existing worker"],
            ["new", "Create the worker with the hire"],
          ]}
        />
      ) : null}
      {creating ? (
        <>
          {text("personnelNumber", "Personnel number")}
          <SelectField
            id="hire-workerType"
            label="Worker type"
            value={values.workerType ?? "EMPLOYEE"}
            onChange={(v) => set({ workerType: v })}
            options={[
              ["EMPLOYEE", "Employee"],
              ["CONTRACTOR", "Contractor"],
            ]}
          />
          {text("partyPersonId", "Party person id")}
          {text("nationalId", "National id")}
          {text("legalName", "Legal name", true, "Leave empty to use the candidate's name.")}
        </>
      ) : (
        text("workerId", "Worker id", false, "The worker record created for this person.")
      )}
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
      <FormActions errors={form.errors} busy={form.busy} submit="Hire from offer" failure={form.failure} failureTitle="Could not hire from the offer" />
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
