import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { createCustomerRole, createVendorRole, setCreditLimit, updateParty, type Party, type Problem } from "./api/client";
import { AMOUNT, CURRENCY, isUuid } from "./format";
import { changedNames, namesOf, NameFieldset, validateNames, type NameValues } from "./nameFields";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useWriteForm, type Errors } from "./useWriteForm";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

function Text({
  id,
  label,
  value,
  error,
  hint,
  optional,
  mono,
  maxLength,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  error?: string;
  hint?: string;
  optional?: boolean;
  mono?: boolean;
  maxLength?: number;
  onChange(value: string): void;
}) {
  return (
    <FormField id={id} label={label} hint={hint} optional={optional} error={error}>
      {(control) => (
        <Input
          {...control}
          value={value}
          maxLength={maxLength}
          className={mono ? styles.mono : undefined}
          onChange={(event) => onChange(event.target.value)}
          invalid={Boolean(error)}
        />
      )}
    </FormField>
  );
}

function Done({ children }: { children?: ReactNode }) {
  return <div role="status">{children ? <Alert tone="success">{children}</Alert> : null}</div>;
}

function requireChoice(errors: Errors, key: string, value: string, label: string, optional = false) {
  if (optional && value.trim() === "") return;
  if (!isUuid(value)) errors[key] = `Choose the ${label}.`;
}

function requireCommon(errors: Errors, accountNumber: string, currencyCode: string) {
  if (accountNumber.trim() === "") errors.accountNumber = "Enter the account number.";
  if (accountNumber.length > 20) errors.accountNumber = "Use at most 20 characters.";
  if (!CURRENCY.test(currencyCode.trim())) errors.currencyCode = "Enter the three-letter currency code, for example EUR.";
}

export function EditPartyCard({ party, onSaved }: { party: Party; onSaved(): void }) {
  const [done, setDone] = useState<ReactNode>(null);
  const original = namesOf(party);
  const base = useRef({ version: party.version, names: original });
  const [clashes, setClashes] = useState<Partial<Record<keyof NameValues, string>>>({});
  const form = useWriteForm<NameValues>(
    original,
    (values) => {
      const errors: Errors = validateNames(party.partyType, values);
      if (Object.keys(errors).length === 0 && Object.keys(changedNames(party.partyType, original, values)).length === 0) {
        errors.form = "Change at least one field before saving.";
      }
      return errors;
    },
    async (values) => {
      setDone(null);
      await updateParty(party.id, party.version, changedNames(party.partyType, original, values));
      setDone("Saved.");
      onSaved();
    },
  );

  const { setValues, clearFailure } = form;
  useEffect(() => {
    if (party.version === base.current.version) return;
    const edited = base.current.names;
    const mine = form.values;
    const keys = Object.keys(mine) as (keyof NameValues)[];
    const clashes = keys.filter((key) => mine[key] !== edited[key] && original[key] !== edited[key] && mine[key].trim() !== original[key]);
    setValues(
      (values) =>
        Object.fromEntries(keys.map((key) => [key, values[key] !== edited[key] ? values[key] : original[key]])) as unknown as NameValues,
    );
    setClashes(
      Object.fromEntries(
        clashes.map((key) => [
          key,
          original[key] === ""
            ? "Cleared by someone else. Saving replaces that."
            : `Changed by someone else to \u201c${original[key]}\u201d. Saving replaces it.`,
        ]),
      ),
    );
    if (clashes.length > 0) {
      setDone(
        `Someone else changed ${clashes.length === 1 ? "a field" : "fields"} you also edited. Your values are kept; review the warnings before saving.`,
      );
      setTimeout(() => document.getElementById(`edit-party-${clashes[0]}`)?.focus(), 0);
    }
    base.current = { version: party.version, names: original };
    clearFailure();
  }, [party.version]);

  // A warning stays only while saving would still replace the other user's value.
  const warnings = Object.fromEntries(
    Object.entries(clashes).filter(([key]) => form.values[key as keyof NameValues].trim() !== original[key as keyof NameValues]),
  );

  return (
    <Card title="Edit party">
      <form ref={form.formRef} className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Edit party">
        <NameFieldset
          type={party.partyType}
          idPrefix="edit-party"
          values={form.values}
          errors={form.errors}
          warnings={warnings}
          onChange={form.set}
        />
        {form.errors.form ? (
          <Alert tone="warning" live="alert">
            {form.errors.form}
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={form.busy}>
            Save changes
          </Button>
        </div>
      </form>
      <Done>{done}</Done>
      {form.failure ? (
        <ProblemAlert
          problem={form.failure}
          title="Could not save the party"
          retry={form.failure.code === "PTY-VERSION-CONFLICT" ? onSaved : undefined}
        />
      ) : null}
    </Card>
  );
}

const vendorBody = (values: typeof emptyVendor) => {
  const paymentTermId = values.paymentTermId.trim();
  return {
    accountNumber: values.accountNumber.trim(),
    vendorGroupId: values.vendorGroupId.trim(),
    currencyCode: values.currencyCode.trim().toUpperCase(),
    ...(paymentTermId ? { paymentTermId } : {}),
  };
};

const customerBody = (values: typeof emptyCustomer) => ({
  accountNumber: values.accountNumber.trim(),
  customerGroupId: values.customerGroupId.trim(),
  paymentTermId: values.paymentTermId.trim(),
  postingProfileId: values.postingProfileId.trim(),
  currencyCode: values.currencyCode.trim().toUpperCase(),
});

const emptyVendor = {
  accountNumber: "",
  vendorGroupId: "",
  paymentTermId: "",
  currencyCode: "",
};

export function VendorRoleCard({ partyId, onCreated }: { partyId: string; onCreated(): void }) {
  const [done, setDone] = useState<ReactNode>(null);
  const form = useWriteForm(
    emptyVendor,
    (values) => {
      const errors: Errors = {};
      requireCommon(errors, values.accountNumber, values.currencyCode);
      if (!isUuid(values.vendorGroupId)) errors.vendorGroupId = "Choose a vendor group.";
      requireChoice(errors, "paymentTermId", values.paymentTermId, "payment term", true);
      return errors;
    },
    async (values, key) => {
      setDone(null);
      await createVendorRole(partyId, vendorBody(values), key);
      setDone(`Vendor role ${values.accountNumber.trim()} added.`);
      form.reset();
      onCreated();
    },
    vendorBody,
  );

  return (
    <Card title="Add a vendor role">
      <form
        ref={form.formRef}
        className={styles.form}
        onSubmit={(event) => void form.submit(event)}
        noValidate
        aria-label="Add a vendor role"
      >
        <Text
          id="vendor-account"
          label="Vendor account number"
          value={form.values.accountNumber}
          error={form.errors.accountNumber}
          maxLength={20}
          mono
          onChange={(v) => form.set("accountNumber", v)}
        />
        <LookupFormField
          id="vendor-group"
          label="Vendor group"
          entity="vendorGroup"
          hint="A group that is not registered yet can be pasted by its id."
          value={form.values.vendorGroupId}
          error={form.errors.vendorGroupId}
          onChange={(v) => form.set("vendorGroupId", v)}
        />
        <LookupFormField
          id="vendor-term"
          label="Vendor payment term"
          entity="paymentTerm"
          optional
          prefill={false}
          value={form.values.paymentTermId}
          error={form.errors.paymentTermId}
          onChange={(v) => form.set("paymentTermId", v)}
        />
        <Text
          id="vendor-currency"
          label="Vendor currency"
          hint="ISO 4217, for example EUR."
          value={form.values.currencyCode}
          error={form.errors.currencyCode}
          maxLength={3}
          mono
          onChange={(v) => form.set("currencyCode", v.toUpperCase())}
        />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={form.busy}>
            Add vendor role
          </Button>
        </div>
      </form>
      <Done>{done}</Done>
      {form.failure ? <ProblemAlert problem={form.failure} title="Could not add the vendor role" /> : null}
    </Card>
  );
}

const emptyCustomer = {
  accountNumber: "",
  customerGroupId: "",
  paymentTermId: "",
  postingProfileId: "",
  currencyCode: "",
};

export function CustomerRoleCard({ partyId, onCreated }: { partyId: string; onCreated(customerId: string): void }) {
  const [done, setDone] = useState<ReactNode>(null);
  const form = useWriteForm(
    emptyCustomer,
    (values) => {
      const errors: Errors = {};
      requireCommon(errors, values.accountNumber, values.currencyCode);
      requireChoice(errors, "customerGroupId", values.customerGroupId, "customer group");
      requireChoice(errors, "paymentTermId", values.paymentTermId, "payment term");
      requireChoice(errors, "postingProfileId", values.postingProfileId, "posting profile");
      return errors;
    },
    async (values, key) => {
      setDone(null);
      const customer = await createCustomerRole(partyId, customerBody(values), key);
      setDone(
        <>
          Customer role {customer.accountNumber} added. Its id is <span className={styles.mono}>{customer.id}</span>; use it below to
          request a credit limit.
        </>,
      );
      form.reset();
      onCreated(customer.id);
    },
    customerBody,
  );

  return (
    <Card title="Add a customer role">
      <form
        ref={form.formRef}
        className={styles.form}
        onSubmit={(event) => void form.submit(event)}
        noValidate
        aria-label="Add a customer role"
      >
        <Text
          id="customer-account"
          label="Customer account number"
          value={form.values.accountNumber}
          error={form.errors.accountNumber}
          maxLength={20}
          mono
          onChange={(v) => form.set("accountNumber", v)}
        />
        <LookupFormField
          id="customer-group"
          label="Customer group"
          entity="customerGroup"
          hint="A group that is not registered yet can be pasted by its id."
          value={form.values.customerGroupId}
          error={form.errors.customerGroupId}
          onChange={(v) => form.set("customerGroupId", v)}
        />
        <LookupFormField
          id="customer-term"
          label="Customer payment term"
          entity="paymentTerm"
          value={form.values.paymentTermId}
          error={form.errors.paymentTermId}
          onChange={(v) => form.set("paymentTermId", v)}
        />
        <LookupFormField
          id="customer-posting"
          label="Posting profile"
          entity="postingProfile"
          value={form.values.postingProfileId}
          error={form.errors.postingProfileId}
          onChange={(v) => form.set("postingProfileId", v)}
        />
        <Text
          id="customer-currency"
          label="Customer currency"
          hint="ISO 4217, for example EUR."
          value={form.values.currencyCode}
          error={form.errors.currencyCode}
          maxLength={3}
          mono
          onChange={(v) => form.set("currencyCode", v.toUpperCase())}
        />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={form.busy}>
            Add customer role
          </Button>
        </div>
      </form>
      <Done>{done}</Done>
      {form.failure ? <ProblemAlert problem={form.failure} title="Could not add the customer role" /> : null}
    </Card>
  );
}

function pendingLink(failure: Problem) {
  const pendingId = failure.code === "PTY-CHANGE-PENDING" ? failure.fields.pendingChangeId : undefined;
  return pendingId ? <Link to={`/party/pending-changes/${pendingId}`}>Open the pending change</Link> : null;
}

export function CreditLimitCard({ initialCustomerId = "" }: { initialCustomerId?: string }) {
  const [done, setDone] = useState<ReactNode>(null);
  const form = useWriteForm(
    { customerId: initialCustomerId, creditLimit: "" },
    (values) => {
      const errors: Errors = {};
      if (!isUuid(values.customerId)) errors.customerId = "Choose the customer.";
      if (!AMOUNT.test(values.creditLimit.trim()))
        errors.creditLimit = "Enter a non-negative amount with at most 2 decimals, for example 25000.00.";
      return errors;
    },
    async (values) => {
      setDone(null);
      const result = await setCreditLimit(values.customerId.trim(), values.creditLimit.trim());
      const held = result.pendingChange;
      setDone(
        held ? (
          <>
            An increase needs approval. The stored limit stays {held.currentValue ?? "unset"} until {held.requestedValue} is approved.{" "}
            <Link to={`/party/pending-changes/${held.id}`}>Open the pending change</Link>
          </>
        ) : (
          `The stored credit limit is ${result.creditLimit ?? "unset"}.`
        ),
      );
    },
  );

  return (
    <Card title="Request a credit limit">
      <form
        ref={form.formRef}
        className={styles.form}
        onSubmit={(event) => void form.submit(event)}
        noValidate
        aria-label="Request a credit limit"
      >
        <LookupFormField
          id="credit-customer"
          label="Customer"
          entity="customer"
          hint="The customer role in your legal entity, not the party."
          value={form.values.customerId}
          error={form.errors.customerId}
          onChange={(v) => form.set("customerId", v)}
        />
        <Text
          id="credit-limit"
          label="Credit limit"
          hint="A decrease applies at once. An increase waits for approval."
          value={form.values.creditLimit}
          error={form.errors.creditLimit}
          mono
          onChange={(v) => form.set("creditLimit", v)}
        />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={form.busy}>
            Set credit limit
          </Button>
        </div>
      </form>
      <Done>{done}</Done>
      {form.failure ? <ProblemAlert problem={form.failure} title="Could not set the credit limit" /> : null}
      {form.failure ? pendingLink(form.failure) : null}
    </Card>
  );
}
