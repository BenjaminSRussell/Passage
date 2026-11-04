import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { createCustomerRole, createVendorRole, setCreditLimit, updateParty, type Party, type Problem } from "./api/client";
import { AMOUNT, CURRENCY, isUuid } from "./format";
import { changedNames, namesOf, NameFieldset, validateNames, type NameValues } from "./nameFields";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useWriteForm, type Errors } from "./useWriteForm";

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

function requireUuid(errors: Errors, key: string, value: string, label: string, optional = false) {
  if (optional && value.trim() === "") return;
  if (!isUuid(value)) errors[key] = `Enter the ${label} (a UUID).`;
}

function requireCommon(errors: Errors, accountNumber: string, currencyCode: string) {
  if (accountNumber.trim() === "") errors.accountNumber = "Enter the account number.";
  if (accountNumber.length > 20) errors.accountNumber = "Use at most 20 characters.";
  if (!CURRENCY.test(currencyCode.trim())) errors.currencyCode = "Enter the three-letter currency code, for example EUR.";
}

export function EditPartyCard({ party, onSaved }: { party: Party; onSaved(): void }) {
  const [done, setDone] = useState<ReactNode>(null);
  const original = namesOf(party);
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

  return (
    <Card title="Edit party">
      <form className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Edit party">
        <NameFieldset type={party.partyType} idPrefix="edit-party" values={form.values} errors={form.errors} onChange={form.set} />
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
      requireUuid(errors, "vendorGroupId", values.vendorGroupId, "vendor group id");
      requireUuid(errors, "paymentTermId", values.paymentTermId, "payment term id", true);
      return errors;
    },
    async (values, key) => {
      setDone(null);
      const paymentTermId = values.paymentTermId.trim();
      await createVendorRole(
        partyId,
        {
          accountNumber: values.accountNumber.trim(),
          vendorGroupId: values.vendorGroupId.trim(),
          currencyCode: values.currencyCode.trim().toUpperCase(),
          ...(paymentTermId ? { paymentTermId } : {}),
        },
        key,
      );
      setDone(`Vendor role ${values.accountNumber.trim()} added.`);
      form.reset();
      onCreated();
    },
  );

  return (
    <Card title="Add a vendor role">
      <form className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Add a vendor role">
        <Text
          id="vendor-account"
          label="Vendor account number"
          value={form.values.accountNumber}
          error={form.errors.accountNumber}
          maxLength={20}
          mono
          onChange={(v) => form.set("accountNumber", v)}
        />
        <Text
          id="vendor-group"
          label="Vendor group id"
          value={form.values.vendorGroupId}
          error={form.errors.vendorGroupId}
          maxLength={36}
          mono
          onChange={(v) => form.set("vendorGroupId", v)}
        />
        <Text
          id="vendor-term"
          label="Vendor payment term id"
          optional
          value={form.values.paymentTermId}
          error={form.errors.paymentTermId}
          maxLength={36}
          mono
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
      requireUuid(errors, "customerGroupId", values.customerGroupId, "customer group id");
      requireUuid(errors, "paymentTermId", values.paymentTermId, "payment term id");
      requireUuid(errors, "postingProfileId", values.postingProfileId, "posting profile id");
      return errors;
    },
    async (values, key) => {
      setDone(null);
      const customer = await createCustomerRole(
        partyId,
        {
          accountNumber: values.accountNumber.trim(),
          customerGroupId: values.customerGroupId.trim(),
          paymentTermId: values.paymentTermId.trim(),
          postingProfileId: values.postingProfileId.trim(),
          currencyCode: values.currencyCode.trim().toUpperCase(),
        },
        key,
      );
      setDone(
        <>
          Customer role {customer.accountNumber} added. Its id is <span className={styles.mono}>{customer.id}</span>; use it below to
          request a credit limit.
        </>,
      );
      form.reset();
      onCreated(customer.id);
    },
  );

  return (
    <Card title="Add a customer role">
      <form className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Add a customer role">
        <Text
          id="customer-account"
          label="Customer account number"
          value={form.values.accountNumber}
          error={form.errors.accountNumber}
          maxLength={20}
          mono
          onChange={(v) => form.set("accountNumber", v)}
        />
        <Text
          id="customer-group"
          label="Customer group id"
          value={form.values.customerGroupId}
          error={form.errors.customerGroupId}
          maxLength={36}
          mono
          onChange={(v) => form.set("customerGroupId", v)}
        />
        <Text
          id="customer-term"
          label="Customer payment term id"
          value={form.values.paymentTermId}
          error={form.errors.paymentTermId}
          maxLength={36}
          mono
          onChange={(v) => form.set("paymentTermId", v)}
        />
        <Text
          id="customer-posting"
          label="Posting profile id"
          value={form.values.postingProfileId}
          error={form.errors.postingProfileId}
          maxLength={36}
          mono
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
      requireUuid(errors, "customerId", values.customerId, "customer role id");
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
      <form className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Request a credit limit">
        <Text
          id="credit-customer"
          label="Customer role id"
          hint="The id of the customer role, not the party."
          value={form.values.customerId}
          error={form.errors.customerId}
          maxLength={36}
          mono
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
