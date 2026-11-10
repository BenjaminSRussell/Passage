import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { newKey } from "../../shell/api/http";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { createProposal, PAYMENT_METHODS, type PaymentMethod } from "./api/client";
import { METHOD_LABELS, today } from "./format";

interface Exception {
  vendorId: string;
  method: PaymentMethod;
}

interface Draft {
  legalEntityId: string;
  currency: string;
  runDate: string;
  dueBy: string;
  method: PaymentMethod;
  exceptions: Exception[];
}

const DAY = /^\d{4}-\d{2}-\d{2}$/;

export function validateDraft(draft: Draft, day: string): Record<string, string> {
  const errors: Record<string, string> = {};
  if (draft.currency === "") errors.currency = "Choose the currency your company reports in.";
  if (!DAY.test(draft.runDate)) errors.runDate = "Enter the payment date.";
  else if (draft.runDate < day) errors.runDate = "The payment date can't be in the past.";
  if (draft.dueBy !== "" && DAY.test(draft.runDate) && draft.dueBy < draft.runDate) errors.dueBy = "This can't be earlier than the payment date.";
  return errors;
}

export function NewProposalPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const runId = useRef(newKey());
  const [draft, setDraft] = useState<Draft>({ legalEntityId: "", currency: "", runDate: today(), dueBy: "", method: "TRANSFER", exceptions: [] });
  const [errors, setErrors] = useState<Record<string, string>>({});

  const create = useMutation({
    mutationFn: createProposal,
    onSuccess: (proposal) => {
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposals"] });
      navigate(`/payables/proposals/${proposal.id}`);
    },
  });

  function edit(patch: Partial<Draft>) {
    runId.current = newKey();
    setDraft((prev) => ({ ...prev, ...patch }));
  }

  function editException(index: number, patch: Partial<Exception>) {
    edit({ exceptions: draft.exceptions.map((x, i) => (i === index ? { ...x, ...patch } : x)) });
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDraft(draft, today());
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const overrides = Object.fromEntries(draft.exceptions.filter((x) => x.vendorId !== "").map((x) => [x.vendorId, x.method]));
    create.mutate({
      legalEntityId: draft.legalEntityId || undefined,
      legalEntityCurrency: draft.currency,
      runDate: draft.runDate,
      runId: runId.current,
      dueBy: draft.dueBy || undefined,
      paymentMethod: draft.method,
      vendorPaymentMethods: Object.keys(overrides).length > 0 ? overrides : undefined,
    });
  }

  const methodOptions = PAYMENT_METHODS.map((m) => (
    <option key={m} value={m}>
      {METHOD_LABELS[m]}
    </option>
  ));

  return (
    <div className={styles.stack}>
      <PageHeader title="New payment proposal" description="Selects every vendor invoice that is due, groups them per vendor and currency, and saves the list as a draft for approval." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New payment proposal">
          <FormField id="np-entity" label="Legal entity" optional hint="Defaults to the entity you are working in.">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={draft.legalEntityId} onChange={(e) => edit({ legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id="np-currency" label="Company currency" error={errors.currency} hint="Totals are shown in this currency.">
            {(c) => <EntityLookup entity="currency" {...c} value={draft.currency} onChange={(e) => edit({ currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="np-run" label="Payment date" error={errors.runDate}>
            {(c) => <Input {...c} type="date" value={draft.runDate} onChange={(e) => edit({ runDate: e.target.value })} invalid={Boolean(errors.runDate)} />}
          </FormField>
          <FormField id="np-due" label="Include invoices due by" optional error={errors.dueBy} hint="Leave empty to include invoices due on the payment date or earlier.">
            {(c) => <Input {...c} type="date" value={draft.dueBy} onChange={(e) => edit({ dueBy: e.target.value })} invalid={Boolean(errors.dueBy)} />}
          </FormField>
          <FormField id="np-method" label="Pay by">
            {(c) => (
              <Select {...c} value={draft.method} onChange={(e) => edit({ method: e.target.value as PaymentMethod })}>
                {methodOptions}
              </Select>
            )}
          </FormField>
          {draft.exceptions.map((x, i) => (
            <fieldset key={i} className={styles.group}>
              <legend className={styles.legend}>{`Vendor exception ${i + 1}`}</legend>
              <FormField id={`np-ex-vendor-${i}`} label={`Vendor ${i + 1}`}>
                {(c) => <EntityLookup entity="vendor" {...c} scope={{ legalEntityId: draft.legalEntityId || undefined }} value={x.vendorId} onChange={(e) => editException(i, { vendorId: e.target.value })} />}
              </FormField>
              <FormField id={`np-ex-method-${i}`} label={`Pay vendor ${i + 1} by`}>
                {(c) => (
                  <Select {...c} value={x.method} onChange={(e) => editException(i, { method: e.target.value as PaymentMethod })}>
                    {methodOptions}
                  </Select>
                )}
              </FormField>
              <div>
                <Button onClick={() => edit({ exceptions: draft.exceptions.filter((_, j) => j !== i) })}>{`Remove vendor ${i + 1}`}</Button>
              </div>
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => edit({ exceptions: [...draft.exceptions, { vendorId: "", method: "TRANSFER" }] })}>Pay a vendor a different way</Button>
          </div>
          <p className={styles.note}>Invoices on hold and vendors blocked for payment are left out. Everything else due is included.</p>
          {create.isError ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the proposal" live="alert">
                <ProblemMessage problem={create.error} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={create.isPending}>
              Create proposal
            </Button>
            <Link className={buttonClassName("ghost")} to="/payables">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
