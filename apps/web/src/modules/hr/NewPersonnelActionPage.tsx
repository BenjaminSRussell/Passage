import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Card, PageHeader } from "../../shell/ui";
import { SKILL_CODE } from "./api/client";
import { submitPersonnelAction, type ActionPayload, type ActionType, type NewPersonnelAction } from "./api/personnel";
import { DATE, UUID, employmentTypeLabel, validFte, validRate } from "./format";
import styles from "./pages.module.css";
import { FormActions, SelectField, TextAreaField, TextField, labelled, opt, useSubmit } from "./parts";

const TYPES: ActionType[] = ["HIRE", "TRANSFER", "TERMINATE", "COMP_CHANGE"];
const EMPLOYMENT_TYPES = Object.entries(employmentTypeLabel);

export interface ActionValues {
  actionType: ActionType;
  workerId: string;
  employmentId: string;
  effectiveDate: string;
  reason: string;
  legalEntityId: string;
  employmentType: string;
  assignmentKind: string;
  positionId: string;
  fteShare: string;
  terminationReason: string;
  compPlanId: string;
  amount: string;
  currency: string;
}

type Errors = Record<string, string>;

const blank: ActionValues = {
  actionType: "HIRE",
  workerId: "",
  employmentId: "",
  effectiveDate: "",
  reason: "",
  legalEntityId: "",
  employmentType: "FULL_TIME",
  assignmentKind: "",
  positionId: "",
  fteShare: "",
  terminationReason: "",
  compPlanId: "",
  amount: "",
  currency: "",
};

/** The service's action rules (FR-42.5): which details each action type takes, and the request the form sends. */
export function validateAction(v: ActionValues): { errors: Errors; input?: NewPersonnelAction } {
  const errors: Errors = {};
  if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the worker's id.";
  if (!DATE.test(v.effectiveDate)) errors.effectiveDate = "Choose the effective date.";
  if (v.reason.trim().length > 500) errors.reason = "Use up to 500 characters.";
  if (v.actionType !== "HIRE" && !UUID.test(v.employmentId.trim())) errors.employmentId = "Enter the employment's id.";

  const details: ActionPayload = {};
  const position = v.positionId.trim();
  if (v.actionType === "TRANSFER" || position !== "") {
    if (!UUID.test(position)) errors.positionId = "Enter the position's id.";
    else details.positionId = position;
  }
  const share = v.fteShare.trim();
  if (share !== "") {
    if (!validFte(share, 1)) errors.fteShare = "Use a decimal above 0 and up to 2.00, with at most two decimals.";
    else details.fteShare = share;
  }
  if (v.actionType === "HIRE") {
    if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id.";
    else details.legalEntityId = v.legalEntityId.trim();
    details.employmentType = v.employmentType;
    if (v.assignmentKind !== "") details.assignmentKind = v.assignmentKind;
  }
  if (v.actionType === "TERMINATE") {
    const reason = v.terminationReason.trim().toUpperCase();
    if (!SKILL_CODE.test(reason)) errors.terminationReason = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
    else details.terminationReason = reason;
  }
  const paying = v.actionType === "COMP_CHANGE" || v.compPlanId.trim() !== "" || v.amount.trim() !== "" || v.currency.trim() !== "";
  if (paying) {
    const currency = v.currency.trim().toUpperCase();
    if (!UUID.test(v.compPlanId.trim())) errors.compPlanId = "Enter the compensation plan's id.";
    if (!/^[A-Z]{3}$/.test(currency)) errors.currency = "Use a three-letter currency code, for example USD.";
    else if (!validRate(v.amount, currency)) errors.amount = "Enter an amount within the currency's decimals.";
    details.compPlanId = v.compPlanId.trim();
    details.currency = currency;
    details.amount = v.amount.trim();
  }
  if (Object.keys(errors).length > 0) return { errors };

  const input: NewPersonnelAction = { actionType: v.actionType, workerId: v.workerId.trim(), effectiveDate: v.effectiveDate, details };
  if (v.actionType !== "HIRE") input.employmentId = v.employmentId.trim();
  const reason = opt(v.reason);
  if (reason) input.reason = reason;
  return { errors, input };
}

/** Submit a hire, transfer, termination or pay change. An action effective today or earlier applies at once. */
export function NewPersonnelActionPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ActionValues>(blank);
  const form = useSubmit({ "HCM-ACT-001": "effectiveDate", "HCM-ACT-006": "effectiveDate", "HCM-ACT-007": "effectiveDate", "HCM-CMP-004": "currency" });

  function update(patch: Partial<ActionValues>) {
    setValues({ ...values, ...patch });
    form.touch();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateAction(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => submitPersonnelAction(input, key), (action) => navigate(`/hr/actions/${action.id}`));
  }

  const text = (name: keyof ActionValues, label: string, extra?: { hint?: string; optional?: boolean; type?: string; maxLength?: number }) => (
    <TextField id={`action-${name}`} label={label} value={values[name]} error={form.errors[name] ?? form.errors[`details.${name}`]} onChange={(v) => update({ [name]: v })} {...extra} />
  );
  const { actionType } = values;
  const showPay = actionType === "HIRE" || actionType === "COMP_CHANGE";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Submit personnel action"
        description="Effective today or earlier, the action applies at once. A later date, up to a year ahead, stays pending and can be withdrawn."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <SelectField
            id="action-actionType"
            label="Action"
            value={actionType}
            error={form.errors.actionType}
            onChange={(v) => update({ actionType: v as ActionType })}
            options={labelled(TYPES)}
          />
          {text("workerId", "Worker id")}
          {actionType !== "HIRE" ? text("employmentId", "Employment id") : null}
          {text("effectiveDate", "Effective date", { type: "date" })}
          {actionType === "HIRE" ? (
            <>
              {text("legalEntityId", "Legal entity id")}
              <SelectField
                id="action-employmentType"
                label="Employment type"
                value={values.employmentType}
                error={form.errors["details.employmentType"]}
                onChange={(v) => update({ employmentType: v })}
                options={EMPLOYMENT_TYPES}
              />
              <SelectField
                id="action-assignmentKind"
                label="Assignment"
                optional
                value={values.assignmentKind}
                error={form.errors["details.assignmentKind"]}
                onChange={(v) => update({ assignmentKind: v })}
                options={labelled(["PRIMARY", "CONCURRENT"])}
                placeholder="Primary"
              />
            </>
          ) : null}
          {actionType === "HIRE" || actionType === "TRANSFER" ? (
            <>
              {text("positionId", "Position id", { optional: actionType === "HIRE" })}
              {text("fteShare", "FTE share", { optional: true, hint: "For example 1.00 or 0.50." })}
            </>
          ) : null}
          {actionType === "TERMINATE" ? text("terminationReason", "Termination reason code", { hint: "For example RESIGN." }) : null}
          {showPay ? (
            <fieldset className={styles.fieldset}>
              <legend className={styles.legend}>{actionType === "HIRE" ? "Pay (optional)" : "New pay"}</legend>
              <p className={styles.note}>Setting pay needs the compensation administrator duty.</p>
              <div className={styles.optionRow}>
                {text("compPlanId", "Compensation plan id")}
                {text("amount", "Amount")}
                {text("currency", "Currency", { maxLength: 3 })}
              </div>
            </fieldset>
          ) : null}
          <div className={styles.wide}>
            <TextAreaField id="action-reason" label="Reason" optional value={values.reason} error={form.errors.reason} maxLength={500} onChange={(v) => update({ reason: v })} />
          </div>
          <FormActions busy={form.busy} submit="Submit action" cancelTo="/hr/actions" failure={form.failure} failureTitle="Could not submit the action" />
        </form>
      </Card>
    </div>
  );
}
