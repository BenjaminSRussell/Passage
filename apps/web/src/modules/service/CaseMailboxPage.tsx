import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  CUSTOMER_REQUIRED,
  describeProblem,
  listCaseCategories,
  listCaseMailboxes,
  listInboundMessages,
  saveCaseMailbox,
  SERVICE_ROUTED,
  type CaseMailbox,
  type CaseMailboxInput,
  type InboundMessage,
  type InboundRoute,
  type Problem,
} from "./api/client";
import { formatDateTime, shortId, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const EMAIL = /^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$/;

const mailboxColumns: DataTableColumn<CaseMailbox & { id: string }>[] = [
  { key: "address", header: "Mailbox", mono: true },
  { key: "categoryCode", header: "Default category", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => shortId(row.legalEntityId) },
  { key: "updatedAt", header: "Updated", mono: true, render: (row) => formatDateTime(row.updatedAt) },
];

const messageColumns: DataTableColumn<InboundMessage>[] = [
  { key: "receivedAt", header: "Received", mono: true, render: (row) => formatDateTime(row.receivedAt) },
  { key: "fromAddress", header: "From", mono: true },
  { key: "subject", header: "Subject", render: (row) => row.subject || "(no subject)" },
  { key: "mailbox", header: "Mailbox", mono: true },
  {
    key: "route",
    header: "Landed on",
    render: (row) =>
      row.route === "ATTACHED" ? (
        <Badge tone="info">{`Attached to ${row.caseNumber}`}</Badge>
      ) : row.reopenedFromNumber ? (
        <Badge tone="warning">{`New case ${row.caseNumber} (continues ${row.reopenedFromNumber})`}</Badge>
      ) : (
        <Badge tone="success">{`New case ${row.caseNumber}`}</Badge>
      ),
  },
  { key: "acknowledge", header: "Acknowledged", render: (row) => (row.acknowledge ? "Yes" : row.automated ? "No (auto-reply)" : "No") },
  { key: "attachments", header: "Files", mono: true, render: (row) => String(row.attachments.length) },
];

export interface MailboxValues {
  address: string;
  legalEntityId: string;
  categoryId: string;
}

/** Checks that mirror the service (intake.go upsertMailbox). */
export function validateMailbox(v: MailboxValues): { errors: Record<string, string>; input?: CaseMailboxInput } {
  const errors: Record<string, string> = {};
  const address = v.address.trim().toLowerCase();
  if (address.length > 320 || !EMAIL.test(address)) errors.address = "Enter the mailbox's email address.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(v.categoryId)) errors.categoryId = "Choose the default category.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { address, legalEntityId: v.legalEntityId.trim(), categoryId: v.categoryId } };
}

/**
 * The case mailbox (PRD 8.45.6): the mailboxes with their default category, setting
 * one up, and the inbound email with the case each message landed on (BR-45.13).
 */
export function CaseMailboxPage() {
  const navigate = useNavigate();
  const mailboxes = useLoad((signal) => listCaseMailboxes(signal), "mailboxes");
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const [mailbox, setMailbox] = useState("");
  const [route, setRoute] = useState<InboundRoute | "">("");
  const [values, setValues] = useState<MailboxValues>({ address: "", legalEntityId: "", categoryId: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  // Email cannot open service cases or cases that need a customer.
  const usable = (categories.data?.items ?? []).filter((c) => !SERVICE_ROUTED.includes(c.categoryType) && !CUSTOMER_REQUIRED.includes(c.categoryType));

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateMailbox(values);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (!input) return;
    setBusy(true);
    try {
      const mb = await saveCaseMailbox(input);
      setSaved(`Mail to ${mb.address} now opens cases in ${mb.categoryCode}.`);
      setValues({ address: "", legalEntityId: "", categoryId: "" });
      mailboxes.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Case mailbox"
        description="Email to a case mailbox lands on the case its [CS-n] subject token or reply chain names; mail to a closed case opens a linked case, and other mail opens a new case in the mailbox's default category."
        actions={
          <Link className={buttonClassName("secondary")} to="/service/case-management">
            Cases
          </Link>
        }
      />
      <Card title="Mailboxes" headingLevel={3}>
        {mailboxes.error ? (
          <Alert tone="danger" title="Could not load the mailboxes" live="alert" action={<Button size="sm" onClick={mailboxes.reload}>Try again</Button>}>
            <ProblemMessage problem={mailboxes.error} />
          </Alert>
        ) : (
          <DataTable
            label="Case mailboxes"
            columns={mailboxColumns}
            rows={(mailboxes.data?.items ?? []).map((m) => ({ ...m, id: m.address }))}
            getRowKey={(row) => row.id}
            loading={mailboxes.loading}
            empty={{ title: "No case mailboxes yet", description: "Set up a mailbox below so email opens cases." }}
            onRowOpen={(row) => setMailbox(row.address)}
          />
        )}
      </Card>
      <Card title="Set up a mailbox" headingLevel={3}>
        {categories.error ? (
          <Alert tone="warning" title="Categories could not be loaded" live="alert">
            <ProblemMessage problem={categories.error} />
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={save} noValidate aria-label="Set up a mailbox">
          <FormField id="mailbox-address" label="Address" hint="Saving an existing address changes its category." error={errors.address}>
            {(control) => (
              <Input {...control} className={styles.mono} value={values.address} onChange={(e) => setValues((v) => ({ ...v, address: e.target.value }))} invalid={Boolean(errors.address)} />
            )}
          </FormField>
          <FormField id="mailbox-legalEntityId" label="Legal entity" hint="UUID of the legal entity that owns the cases." error={errors.legalEntityId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.legalEntityId}
                onChange={(e) => setValues((v) => ({ ...v, legalEntityId: e.target.value }))}
                invalid={Boolean(errors.legalEntityId)}
              />
            )}
          </FormField>
          <FormField id="mailbox-categoryId" label="Default category" hint="Not a service or collections category." error={errors.categoryId}>
            {(control) => (
              <Select {...control} value={values.categoryId} onChange={(e) => setValues((v) => ({ ...v, categoryId: e.target.value }))} invalid={Boolean(errors.categoryId)}>
                <option value="">{categories.loading ? "Loading…" : "Choose a category"}</option>
                {usable.map((c) => (
                  <option key={c.id} value={c.id}>
                    {`${c.code} — ${c.name}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the mailbox" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" title={saved} live="status" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save mailbox
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Inbound email" headingLevel={3}>
        <div className={styles.stack}>
          <div className={styles.toolbar}>
            <FormField id="inbound-mailbox" label="Mailbox">
              {(control) => (
                <Select {...control} value={mailbox} onChange={(e) => setMailbox(e.target.value)}>
                  <option value="">Every mailbox</option>
                  {(mailboxes.data?.items ?? []).map((m) => (
                    <option key={m.address} value={m.address}>
                      {m.address}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="inbound-route" label="Landed on">
              {(control) => (
                <Select {...control} value={route} onChange={(e) => setRoute(e.target.value as InboundRoute | "")}>
                  <option value="">Any</option>
                  <option value="NEW_CASE">A new case</option>
                  <option value="ATTACHED">An open case</option>
                </Select>
              )}
            </FormField>
          </div>
          <PagedTable
            label="Inbound email"
            columns={messageColumns}
            load={(cursor, signal) => listInboundMessages({ mailbox, route, cursor }, signal)}
            loadKey={`${mailbox}|${route}`}
            empty={{ title: "No inbound email", description: "Email the integration hub hands over appears here with the case it landed on." }}
            onRowOpen={(row) => navigate(`/service/case-management/${row.caseId}`)}
          />
        </div>
      </Card>
    </div>
  );
}
