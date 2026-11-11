import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { usePrincipalDuties } from "../../shell/principal";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  FormErrorSummary,
  FormField,
  FormSection,
  Input,
  PageHeader,
  Select,
  Skeleton,
  summaryItems,
  Textarea,
} from "../../shell/ui";
import { acceptProspect, describeProblem, getProspect, listQuestionnaires, rejectProspect, type Problem, type Prospect } from "./api/client";
import { CURRENCY, DUTY_READ, DUTY_WRITE, formatDateTime, isUuid } from "./format";
import styles from "./pages.module.css";
import { Entry, problemText, ProblemAlert } from "./parts";
import { prospectStatusLabel, prospectStatusTone } from "./ProspectsPage";
import { useLoad } from "./useLoad";

const DUTY_DECIDE = "party.vendor-request.decide";
const NOTE_MAX = 1000;
const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const BACK = "/party/vendor-prospects";

type Outcome = { tone: "success"; text: string } | { tone: "danger"; problem: Problem };

interface AcceptValues {
  accountNumber: string;
  vendorGroupId: string;
  currencyCode: string;
  questionnaireId: string;
}

const ACCEPT_FIELDS = {
  accountNumber: { id: "prospect-account", label: "Vendor account number" },
  vendorGroupId: { id: "prospect-group", label: "Vendor group" },
  currencyCode: { id: "prospect-currency", label: "Vendor currency" },
  questionnaireId: { id: "prospect-questionnaire", label: "Onboarding questionnaire" },
};

/** Checks that mirror the party service (domain.NewVendor.Validate and the accept handler). */
export function validateAccept(values: AcceptValues): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!ACCOUNT.test(values.accountNumber.trim())) errors.accountNumber = "Enter 1 to 20 letters, digits, dots, dashes or underscores.";
  if (!isUuid(values.vendorGroupId)) errors.vendorGroupId = "Choose the vendor group.";
  if (!CURRENCY.test(values.currencyCode.trim())) errors.currencyCode = "Enter the three-letter currency code, for example USD.";
  if (!isUuid(values.questionnaireId)) errors.questionnaireId = "Choose the questionnaire the supplier answers.";
  return errors;
}

/** One supplier registration: its details, then accept it as a vendor (party.write) or reject it (party.vendor-request.decide). */
export function ProspectDetailPage() {
  const { id = "" } = useParams();
  return <ProspectDetail key={id} id={id} />;
}

function ProspectDetail({ id }: { id: string }) {
  const duties = usePrincipalDuties();
  const state = useLoad((signal) => getProspect(id, signal), id);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const outcomeRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (outcome) outcomeRef.current?.focus();
  }, [outcome]);

  const header = (prospect?: Prospect) => (
    <PageHeader
      title={prospect ? prospect.companyName : "Supplier registration"}
      description="A company that registered itself on the supplier portal."
      meta={prospect ? <Badge tone={prospectStatusTone[prospect.status]}>{prospectStatusLabel[prospect.status]}</Badge> : undefined}
      actions={
        <Link className={buttonClassName("ghost")} to={BACK}>
          Back to supplier registrations
        </Link>
      }
    />
  );

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        {header()}
        <ProblemAlert problem={state.error} title="Could not load the registration" retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header()}
        <span className="visually-hidden">Loading the registration…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const prospect = state.data;
  const done = (text: string) => {
    setOutcome({ tone: "success", text });
    state.reload();
  };
  const failed = (failure: unknown) => setOutcome({ tone: "danger", problem: describeProblem(failure) });

  return (
    <div className={styles.stack}>
      {header(prospect)}
      <div role="status" ref={outcomeRef} tabIndex={-1}>
        {outcome?.tone === "danger" ? (
          <Alert tone="danger" title="The decision failed">
            <ProblemMessage problem={outcome.problem} message={problemText(outcome.problem)} />
          </Alert>
        ) : null}
        {outcome?.tone === "success" ? <Alert tone="success">{outcome.text}</Alert> : null}
      </div>
      <Card title="Registration">
        <dl className={styles.details} aria-label="Registration">
          <Entry term="Company">{prospect.companyName}</Entry>
          <Entry term="Contact">{prospect.contactName}</Entry>
          <Entry term="Email">{prospect.email}</Entry>
          {prospect.phone ? <Entry term="Phone">{prospect.phone}</Entry> : null}
          <Entry term="Country">{prospect.country}</Entry>
          {prospect.taxId ? <Entry term="Tax number">{prospect.taxId}</Entry> : null}
          {prospect.website ? <Entry term="Website">{prospect.website}</Entry> : null}
          <Entry term="Registered">{formatDateTime(prospect.createdAt)}</Entry>
          {prospect.message ? <Entry term="Message">{prospect.message}</Entry> : null}
        </dl>
      </Card>
      {prospect.status === "NEW" ? (
        <>
          {holdsDuty(duties, DUTY_WRITE) ? (
            <AcceptCard prospect={prospect} onDone={() => done("Accepted. The party and a blocked vendor role were created, and the supplier was asked to answer the questionnaire.")} onFailed={failed} />
          ) : (
            <p className={styles.muted}>Accepting a registration needs the party.write duty.</p>
          )}
          {holdsDuty(duties, DUTY_DECIDE) ? (
            <RejectCard prospect={prospect} onDone={() => done("Rejected. Nothing was created; the note is kept with the registration.")} onFailed={failed} />
          ) : null}
        </>
      ) : (
        <Decision prospect={prospect} />
      )}
    </div>
  );
}

function Decision({ prospect }: { prospect: Prospect }) {
  const canRead = holdsDuty(usePrincipalDuties(), DUTY_READ);
  return (
    <Card title="Decision">
      <dl className={styles.details} aria-label="Decision">
        <Entry term="Decided">{formatDateTime(prospect.decidedAt)}</Entry>
        {prospect.decisionNote ? <Entry term="Note">{prospect.decisionNote}</Entry> : null}
        {prospect.vendorId ? (
          <Entry term="Vendor">
            <EntityName entity="vendor" id={prospect.vendorId} fallback="Vendor" />
          </Entry>
        ) : null}
      </dl>
      {prospect.partyId && canRead ? (
        <Link className={buttonClassName("secondary")} to={`/party/${encodeURIComponent(prospect.partyId)}`}>
          Open the party
        </Link>
      ) : null}
      {prospect.status === "ACCEPTED" ? (
        <p className={styles.note}>The vendor stays blocked until someone other than the buyer who accepted it approves the onboarding questionnaire.</p>
      ) : null}
    </Card>
  );
}

function AcceptCard({ prospect, onDone, onFailed }: { prospect: Prospect; onDone(): void; onFailed(failure: unknown): void }) {
  const questionnaires = useLoad((signal) => listQuestionnaires(signal), "questionnaires");
  const [values, setValues] = useState<AcceptValues>({ accountNumber: "", vendorGroupId: "", currencyCode: "", questionnaireId: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const set = (key: keyof AcceptValues, value: string) => setValues((v) => ({ ...v, [key]: value }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAccept(values);
    setErrors(found);
    setAttempt((n) => n + 1);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await acceptProspect(prospect.id, {
        accountNumber: values.accountNumber.trim(),
        vendorGroupId: values.vendorGroupId.trim(),
        currencyCode: values.currencyCode.trim().toUpperCase(),
        questionnaireId: values.questionnaireId,
      });
      onDone();
    } catch (failure) {
      const fields = describeProblem(failure).fields;
      if (Object.keys(fields).length > 0) {
        setErrors(fields);
        setAttempt((n) => n + 1);
      }
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  const options = questionnaires.data ?? [];
  return (
    <Card title="Accept as a vendor">
      <form className={styles.form} onSubmit={(event) => void submit(event)} noValidate aria-label="Accept as a vendor">
        <FormErrorSummary items={summaryItems(errors, ACCEPT_FIELDS)} attempt={attempt} />
        <FormSection title="Vendor role" description={`The party is created as ${prospect.companyName}${prospect.taxId ? ` with registration number ${prospect.taxId}` : ""}.`}>
          <FormField id={ACCEPT_FIELDS.accountNumber.id} label={ACCEPT_FIELDS.accountNumber.label} error={errors.accountNumber}>
            {(control) => (
              <Input {...control} value={values.accountNumber} maxLength={20} className={styles.mono} onChange={(e) => set("accountNumber", e.target.value)} invalid={Boolean(errors.accountNumber)} />
            )}
          </FormField>
          <LookupFormField
            id={ACCEPT_FIELDS.vendorGroupId.id}
            label={ACCEPT_FIELDS.vendorGroupId.label}
            entity="vendorGroup"
            value={values.vendorGroupId}
            error={errors.vendorGroupId}
            onChange={(v) => set("vendorGroupId", v)}
          />
          <FormField id={ACCEPT_FIELDS.currencyCode.id} label={ACCEPT_FIELDS.currencyCode.label} hint="ISO 4217, for example USD." error={errors.currencyCode}>
            {(control) => (
              <Input {...control} value={values.currencyCode} maxLength={3} className={styles.mono} onChange={(e) => set("currencyCode", e.target.value.toUpperCase())} invalid={Boolean(errors.currencyCode)} />
            )}
          </FormField>
        </FormSection>
        <FormSection title="Onboarding" description="The supplier answers this questionnaire in the portal; the vendor is blocked until the answers are approved.">
          <FormField id={ACCEPT_FIELDS.questionnaireId.id} label={ACCEPT_FIELDS.questionnaireId.label} error={errors.questionnaireId ?? (questionnaires.error ? problemText(questionnaires.error) : undefined)}>
            {(control) => (
              <Select {...control} value={values.questionnaireId} onChange={(e) => set("questionnaireId", e.target.value)} invalid={Boolean(errors.questionnaireId)}>
                <option value="">{questionnaires.loading ? "Loading questionnaires…" : "Choose a questionnaire"}</option>
                {options.map((q) => (
                  <option key={q.id} value={q.id}>
                    {`${q.name} (version ${q.version})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </FormSection>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Accept and create vendor
          </Button>
        </div>
      </form>
    </Card>
  );
}

function RejectCard({ prospect, onDone, onFailed }: { prospect: Prospect; onDone(): void; onFailed(failure: unknown): void }) {
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  function ask(event: FormEvent) {
    event.preventDefault();
    const text = note.trim();
    if (text === "" || text.length > NOTE_MAX) {
      setError(`Enter a note of at most ${NOTE_MAX} characters.`);
      return;
    }
    setError("");
    setConfirming(true);
  }

  async function reject() {
    setConfirming(false);
    setBusy(true);
    try {
      await rejectProspect(prospect.id, note.trim());
      setNote("");
      onDone();
    } catch (failure) {
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Reject">
      <form className={styles.form} onSubmit={ask} noValidate aria-label="Reject the registration">
        <FormField id="prospect-note" label="Note" hint={`Why the registration is rejected. At most ${NOTE_MAX} characters.`} error={error || undefined}>
          {(control) => <Textarea {...control} rows={3} maxLength={NOTE_MAX} value={note} onChange={(e) => setNote(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" loading={busy}>
            Reject registration
          </Button>
        </div>
      </form>
      {confirming ? (
        <ConfirmDialog
          title={`Reject the registration of ${prospect.companyName}?`}
          description="No party or vendor is created. The registration is closed with your note."
          confirmLabel="Confirm rejection"
          cancelLabel="Cancel"
          onCancel={() => setConfirming(false)}
          onConfirm={() => void reject()}
        />
      ) : null}
    </Card>
  );
}
