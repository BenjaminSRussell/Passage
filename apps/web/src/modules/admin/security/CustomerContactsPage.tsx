import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { EntityName } from "../../../shell/lookup/EntityName";
import { LookupFormField } from "../../../shell/lookup/LookupFormField";
import { usePrincipalDuties } from "../../../shell/principal";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatusPill, type DataTableColumn } from "../../../shell/ui";
import {
  disableCustomerContact,
  inviteCustomerContact,
  listCustomerContacts,
  listLegalEntities,
  remapCustomerContact,
  resendCustomerContactInvitation,
  UUID,
  type CustomerContact,
  type LegalEntity,
} from "./api";
import styles from "./security.module.css";
import { ApiFailure, canRetry, fieldsOwnFailure, mapServerFields, useFocusFirstInvalid, withoutFields, type Errors } from "./support";

const DUTY = "identity.customer-contact.maintain";
const FIELDS = ["email", "givenName", "familyName", "customerId", "legalEntityIds"] as const;
const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

async function listAllLegalEntities(): Promise<LegalEntity[]> {
  const all: LegalEntity[] = [];
  let after: string | undefined;
  do {
    const page = await listLegalEntities(after);
    all.push(...page.items.filter((e) => e.status === "active"));
    after = page.next;
  } while (after);
  return all;
}

interface Draft {
  email: string;
  givenName: string;
  familyName: string;
  customerId: string;
  legalEntityIds: string[];
}

const EMPTY: Draft = { email: "", givenName: "", familyName: "", customerId: "", legalEntityIds: [] };

function validate(draft: Draft, editing: boolean): Errors {
  const errors: Errors = {};
  if (!editing && !EMAIL.test(draft.email.trim())) errors.email = "Enter the contact's email address.";
  if (!UUID.test(draft.customerId)) errors.customerId = "Choose the customer this contact orders for.";
  if (draft.legalEntityIds.length === 0) errors.legalEntityIds = "Choose at least one company the contact may deal with.";
  return errors;
}

/**
 * Customer portal contacts: invite a person to the customer portal and say which customer and companies they act for. The
 * person gets an email to confirm the address, choose a password and register an authenticator app; changing the mapping signs
 * them out so their next sign-in carries the new customer.
 */
export function CustomerContactsPage() {
  const queryClient = useQueryClient();
  const canMaintain = usePrincipalDuties().includes(DUTY);
  const form = useRef<HTMLFormElement>(null);
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [editing, setEditing] = useState<CustomerContact | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [saved, setSaved] = useState<string | null>(null);
  const contacts = useInfiniteQuery({
    queryKey: ["admin", "customer-contacts"],
    queryFn: ({ pageParam }) => listCustomerContacts(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
    retry: false,
  });
  const entities = useQuery({ queryKey: ["admin", "legal-entities", "active-options"], queryFn: listAllLegalEntities, retry: false });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["admin", "customer-contacts"] });
  const save = useMutation({
    mutationFn: (v: Draft) =>
      editing
        ? remapCustomerContact(editing.contactId, { customerId: v.customerId, legalEntityIds: v.legalEntityIds })
        : inviteCustomerContact({ email: v.email.trim(), givenName: v.givenName.trim() || undefined, familyName: v.familyName.trim() || undefined, customerId: v.customerId, legalEntityIds: v.legalEntityIds }),
    onSuccess: async (stored) => {
      setSaved(editing ? `${stored.email} now acts for the chosen customer; they were signed out` : stored.invitationSent ? `Invitation sent to ${stored.email}` : `${stored.email} was added, but the invitation email could not be sent. Use Resend invitation.`);
      setDraft(EMPTY);
      setEditing(null);
      await refresh();
    },
    onError: (error) => setErrors(mapServerFields(error, FIELDS)),
  });
  const action = useMutation({
    mutationFn: (a: { kind: "resend" | "disable"; contact: CustomerContact }) => (a.kind === "resend" ? resendCustomerContactInvitation(a.contact.contactId) : disableCustomerContact(a.contact.contactId)),
    onSuccess: async (stored, a) => {
      setSaved(a.kind === "resend" ? `Invitation sent again to ${stored.email}` : `${stored.email} was disabled and signed out`);
      await refresh();
    },
  });
  useFocusFirstInvalid(form, errors);

  function change(next: Partial<Draft>) {
    setDraft({ ...draft, ...next });
    setErrors(withoutFields(errors, Object.keys(next)));
    setSaved(null);
    save.reset();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validate(draft, editing !== null);
    setErrors(found);
    if (Object.keys(found).length === 0) save.mutate(draft);
  }

  function edit(contact: CustomerContact) {
    setEditing(contact);
    setDraft({ ...EMPTY, email: contact.email, customerId: contact.customerId, legalEntityIds: contact.legalEntityIds });
    setErrors({});
    setSaved(null);
    form.current?.scrollIntoView?.({ block: "start" });
  }

  const columns: DataTableColumn<CustomerContact>[] = [
    { key: "email", header: "Contact", render: (c) => (c.displayName ? `${c.displayName} (${c.email})` : c.email) },
    { key: "customerId", header: "Customer", render: (c) => <EntityName entity="customer" id={c.customerId} /> },
    {
      key: "legalEntityIds",
      header: "Companies",
      render: (c) => (
        <>
          {c.legalEntityIds.map((id, n) => (
            <span key={id}>
              {n > 0 ? ", " : ""}
              <EntityName entity="legalEntity" id={id} />
            </span>
          ))}
        </>
      ),
    },
    { key: "status", header: "Status", render: (c) => <StatusPill status={c.status} /> },
    { key: "invitationSent", header: "Invitation", render: (c) => (c.invitationSent ? "Sent" : "Not sent") },
    ...(canMaintain
      ? [
          {
            key: "actions",
            header: "Actions",
            render: (c: CustomerContact) =>
              c.status === "disabled" ? (
                "None"
              ) : (
                <span className={styles.rowActions}>
                  <Button size="sm" variant="secondary" onClick={() => edit(c)}>
                    Change {c.email}
                  </Button>
                  <Button size="sm" variant="secondary" loading={action.isPending} onClick={() => action.mutate({ kind: "resend", contact: c })}>
                    Resend invitation to {c.email}
                  </Button>
                  <Button size="sm" variant="secondary" loading={action.isPending} onClick={() => action.mutate({ kind: "disable", contact: c })}>
                    Disable {c.email}
                  </Button>
                </span>
              ),
          } satisfies DataTableColumn<CustomerContact>,
        ]
      : []),
  ];

  const rows = contacts.data?.pages.flatMap((page) => page.items) ?? [];
  const saveFailure = save.isError && !fieldsOwnFailure(save.error, FIELDS);

  return (
    <div className={styles.stack}>
      <PageHeader title="Customer portal contacts" description="The people who sign in to the customer portal, and the customer and companies each one acts for." />
      {canMaintain ? (
        <Card title={editing ? `Change ${editing.email}` : "Invite a contact"}>
          <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Change contact" : "Invite a contact"} ref={form}>
            <fieldset className={styles.fields} disabled={save.isPending}>
              {editing ? null : (
                <>
                  <FormField id="cc-email" label="Email address" hint="They receive a link to confirm it, choose a password and register an authenticator app." error={errors.email}>
                    {(c) => <Input {...c} type="email" value={draft.email} maxLength={254} onChange={(e) => change({ email: e.target.value })} invalid={Boolean(errors.email)} autoComplete="off" />}
                  </FormField>
                  <FormField id="cc-given" label="First name" optional error={errors.givenName}>
                    {(c) => <Input {...c} value={draft.givenName} maxLength={100} onChange={(e) => change({ givenName: e.target.value })} />}
                  </FormField>
                  <FormField id="cc-family" label="Last name" optional error={errors.familyName}>
                    {(c) => <Input {...c} value={draft.familyName} maxLength={100} onChange={(e) => change({ familyName: e.target.value })} />}
                  </FormField>
                </>
              )}
              <LookupFormField id="cc-customer" label="Customer" entity="customer" value={draft.customerId} required onChange={(value) => change({ customerId: value })} hint="Orders, invoices and payments of this customer." error={errors.customerId} />
              <fieldset className={styles.checks} aria-describedby={errors.legalEntityIds ? "cc-companies-error" : undefined}>
                <legend>Companies</legend>
                {(entities.data ?? []).map((entity) => (
                  <label key={entity.legalEntityId} htmlFor={`cc-company-${entity.legalEntityId}`}>
                    <input
                      id={`cc-company-${entity.legalEntityId}`}
                      type="checkbox"
                      checked={draft.legalEntityIds.includes(entity.legalEntityId)}
                      onChange={(e) => change({ legalEntityIds: e.target.checked ? [...draft.legalEntityIds, entity.legalEntityId] : draft.legalEntityIds.filter((id) => id !== entity.legalEntityId) })}
                      aria-invalid={errors.legalEntityIds ? "true" : undefined}
                    />
                    {entity.displayName}
                  </label>
                ))}
                {errors.legalEntityIds ? <span id="cc-companies-error" role="alert">{errors.legalEntityIds}</span> : null}
              </fieldset>
            </fieldset>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={save.isPending}>
                {editing ? "Save mapping" : "Send invitation"}
              </Button>
              {editing ? (
                <Button type="button" variant="secondary" onClick={() => { setEditing(null); setDraft(EMPTY); setErrors({}); }}>
                  Cancel
                </Button>
              ) : null}
            </div>
          </form>
        </Card>
      ) : null}
      <Alert tone="success" live="status" title={saved ?? undefined} />
      {saveFailure ? <ApiFailure error={save.error} title={editing ? "Could not change the contact" : "Could not invite the contact"} duty={DUTY} /> : null}
      {action.isError ? <ApiFailure error={action.error} title="Could not complete that action" duty={DUTY} /> : null}
      {contacts.isError ? (
        <ApiFailure
          error={contacts.error}
          title="Could not load the contacts"
          duty={DUTY}
          action={canRetry(contacts.error) ? <Button variant="secondary" loading={contacts.isFetching} onClick={() => void contacts.refetch()}>Try again</Button> : undefined}
        />
      ) : (
        <>
          <DataTable label="Customer portal contacts" columns={columns} rows={rows} getRowKey={(c) => c.contactId} loading={contacts.isPending} empty={{ title: "No contacts yet", description: "Invite the first person to the customer portal." }} />
          {contacts.hasNextPage ? (
            <div className={styles.formActions}>
              <Button variant="secondary" loading={contacts.isFetchingNextPage} onClick={() => void contacts.fetchNextPage()}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
