import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  closeInstrument,
  createFacility,
  createInstrument,
  describeProblem,
  getFacility,
  INSTRUMENT_KINDS,
  listFacilities,
  listInstruments,
  newKey,
  TreasuryApiError,
  type Facility,
  type FacilityInput,
  type FacilityInstrument,
  type InstrumentInput,
  type InstrumentKind,
  type Problem,
} from "./api/client";
import { ACCOUNT_CODE, CURRENCY, DAY, money, UUID, validAmount } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

export const KIND_LABELS: Record<InstrumentKind, string> = {
  lc_import: "Import letter of credit",
  lc_export: "Export letter of credit",
  guarantee: "Bank guarantee",
};

const facilityColumns: DataTableColumn<Facility>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "bankName", header: "Bank" },
  { key: "limit", header: "Limit", mono: true, align: "end", render: (row) => money(row.limit, row.currency) },
  { key: "utilized", header: "Utilized", mono: true, align: "end", render: (row) => money(row.utilized, row.currency) },
  { key: "headroom", header: "Headroom", mono: true, align: "end", render: (row) => money(row.headroom, row.currency) },
  { key: "expiryDate", header: "Expires", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Bank facilities of one legal entity with utilization (FR-36.7, facility utilization inquiry). */
export function FacilitiesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const [draft, setDraft] = useState(legalEntityId);
  const [error, setError] = useState<string | undefined>();
  const chosen = UUID.test(legalEntityId);
  const state = useLoad((signal) => (chosen ? listFacilities(legalEntityId, signal) : Promise.resolve([])), legalEntityId);

  function choose(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!UUID.test(value)) {
      setError("Enter the legal entity id (a UUID).");
      return;
    }
    setError(undefined);
    setParams({ legalEntityId: value });
  }

  const newLink = chosen ? `/treasury/facilities/new?legalEntityId=${encodeURIComponent(legalEntityId)}` : "/treasury/facilities/new";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Facilities"
        description="Bank credit lines that letters of credit and guarantees draw on. Utilization is the sum of the open instruments."
        actions={
          <Link className={buttonClassName("primary")} to={newLink}>
            New facility
          </Link>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={choose} noValidate>
          <FormField id="fa-entity" label="Legal entity id" error={error}>
            {(c) => <Input {...c} className={styles.mono} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button type="submit">Show facilities</Button>
        </form>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="Facilities belong to one legal entity. Enter its id to list them." />
        </Card>
      ) : state.error ? (
        <LoadFailure title="Could not load the facilities" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Facilities"
          columns={facilityColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No facilities yet", description: "Register the legal entity's first bank facility." }}
          onRowOpen={(row) => navigate(`/treasury/facilities/${row.id}`)}
        />
      )}
    </div>
  );
}

export interface FacilityDraft {
  legalEntityId: string;
  code: string;
  bankName: string;
  currency: string;
  limit: string;
  expiryDate: string;
}

export function validateFacility(v: FacilityDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!ACCOUNT_CODE.test(v.code.trim())) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  const bank = v.bankName.trim();
  if (bank.length < 1 || bank.length > 120) errors.bankName = "Enter the bank's name, up to 120 characters.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter an ISO 4217 code such as EUR.";
  if (!validAmount(v.limit, v.currency)) errors.limit = "Enter a positive limit with no more decimals than the currency allows.";
  if (!DAY.test(v.expiryDate)) errors.expiryDate = "Choose the date the facility expires.";
  return errors;
}

export function toFacilityInput(v: FacilityDraft): FacilityInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    code: v.code.trim(),
    bankName: v.bankName.trim(),
    currency: v.currency.trim(),
    limit: v.limit.trim(),
    expiryDate: v.expiryDate,
  };
}

/** Register a facility. A retry after a network failure reuses the Idempotency-Key. */
export function NewFacilityPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<FacilityDraft>({
    legalEntityId: params.get("legalEntityId") ?? "",
    code: "",
    bankName: "",
    currency: "",
    limit: "",
    expiryDate: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFacility(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createFacility(toFacilityInput(values), key.current);
      navigate(`/treasury/facilities/${created.id}`);
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else if (problem.code === "TRS-0427") setErrors({ code: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof FacilityDraft, label: string, opts: { hint?: string; mono?: boolean; type?: string } = {}) => (
    <FormField id={`fa-${id}`} label={label} hint={opts.hint} error={errors[id]}>
      {(c) => (
        <Input
          {...c}
          type={opts.type}
          className={opts.mono === false ? undefined : styles.mono}
          inputMode={id === "limit" ? "decimal" : undefined}
          value={values[id]}
          onChange={(e) => setValues((prev) => ({ ...prev, [id]: e.target.value }))}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New facility"
        description="A bank credit line with a limit and an expiry date. Instruments in its currency draw on it."
        actions={
          <Link className={buttonClassName("secondary")} to="/treasury/facilities">
            Facilities
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("legalEntityId", "Legal entity id")}
          {field("code", "Facility code", { hint: "Unique per legal entity, for example LC-DB-01." })}
          {field("bankName", "Bank", { mono: false })}
          {field("currency", "Currency", { hint: "ISO 4217, for example EUR." })}
          {field("limit", "Limit", { hint: "For example 1000000.00." })}
          {field("expiryDate", "Expiry date", { type: "date" })}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not create the facility" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create facility
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

export interface InstrumentDraft {
  kind: InstrumentKind;
  reference: string;
  beneficiary: string;
  amount: string;
  issueDate: string;
  expiryDate: string;
  linkedOrderRef: string;
  overrideLimit: boolean;
}

export function validateInstrument(v: InstrumentDraft, currency: string): Errors {
  const errors: Errors = {};
  const ref = v.reference.trim();
  if (ref.length < 1 || ref.length > 64) errors.reference = "Enter the bank's reference, up to 64 characters.";
  const ben = v.beneficiary.trim();
  if (ben.length < 1 || ben.length > 140) errors.beneficiary = "Enter the beneficiary, up to 140 characters.";
  if (!validAmount(v.amount, currency)) errors.amount = `Enter a positive amount in ${currency}.`;
  if (!DAY.test(v.issueDate)) errors.issueDate = "Choose the issue date.";
  if (!DAY.test(v.expiryDate)) errors.expiryDate = "Choose the expiry date.";
  else if (DAY.test(v.issueDate) && v.expiryDate < v.issueDate) errors.expiryDate = "The expiry date cannot be before the issue date.";
  if (v.linkedOrderRef.trim().length > 64) errors.linkedOrderRef = "Use up to 64 characters.";
  return errors;
}

export function toInstrumentInput(v: InstrumentDraft, currency: string): InstrumentInput {
  const input: InstrumentInput = {
    kind: v.kind,
    reference: v.reference.trim(),
    beneficiary: v.beneficiary.trim(),
    currency,
    amount: v.amount.trim(),
    issueDate: v.issueDate,
    expiryDate: v.expiryDate,
  };
  if (v.linkedOrderRef.trim() !== "") input.linkedOrderRef = v.linkedOrderRef.trim();
  if (v.overrideLimit) input.overrideLimit = true;
  return input;
}

const emptyInstrument: InstrumentDraft = {
  kind: "lc_import",
  reference: "",
  beneficiary: "",
  amount: "",
  issueDate: "",
  expiryDate: "",
  linkedOrderRef: "",
  overrideLimit: false,
};

/** One facility: limit, utilization and headroom, its instruments, booking a new one and closing open ones. */
export function FacilityDetailPage() {
  const { id = "" } = useParams();
  const facility = useLoad((signal) => getFacility(id, signal), `facility-${id}`);
  const instruments = useLoad((signal) => listInstruments(id, signal), `instruments-${id}`);
  const [draft, setDraft] = useState<InstrumentDraft>(emptyInstrument);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [done, setDone] = useState("");
  const key = useRef(newKey());

  function reloadAll() {
    facility.reload();
    instruments.reload();
  }

  async function book(event: FormEvent, f: Facility) {
    event.preventDefault();
    const found = validateInstrument(draft, f.currency);
    setErrors(found);
    setFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setBusy("book");
    try {
      const created = await createInstrument(f.id, toInstrumentInput(draft, f.currency), key.current);
      key.current = newKey();
      setDraft(emptyInstrument);
      setDone(`${KIND_LABELS[created.kind]} ${created.reference} is booked for ${money(created.amount, created.currency)}${created.limitOverride ? " past the limit, under the override" : ""}.`);
      reloadAll();
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else if (problem.code === "TRS-0415") setFailure({ title: "The facility limit would be exceeded", problem });
      else if (problem.code === "AUTH-0003") setFailure({ title: "You cannot override the facility limit", problem });
      else setFailure({ title: "Could not book the instrument", problem });
    } finally {
      setBusy(null);
    }
  }

  async function close(inst: FacilityInstrument) {
    setFailure(null);
    setDone("");
    setBusy(inst.id);
    try {
      const closed = await closeInstrument(inst.id, inst.version);
      setDone(`${closed.reference} is closed and no longer counts against the limit.`);
      reloadAll();
    } catch (error) {
      const stale = error instanceof TreasuryApiError && error.status === 412;
      setFailure({ title: stale ? "This instrument changed since you opened it" : `Could not close ${inst.reference}`, problem: describeProblem(error) });
      if (stale) instruments.reload();
    } finally {
      setBusy(null);
    }
  }

  if (facility.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Facility" />
        <LoadFailure title="Could not load the facility" problem={facility.error} retry={facility.reload} />
      </div>
    );
  }
  const f = facility.data;
  if (!f) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Facility" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const instrumentColumns: DataTableColumn<FacilityInstrument>[] = [
    { key: "reference", header: "Reference", mono: true },
    { key: "kind", header: "Kind", render: (row) => KIND_LABELS[row.kind] ?? row.kind },
    { key: "beneficiary", header: "Beneficiary" },
    { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
    { key: "expiryDate", header: "Expires", mono: true },
    { key: "limitOverride", header: "Override", render: (row) => (row.limitOverride ? "Yes" : "No") },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "open" ? (
          <Button size="sm" loading={busy === row.id} onClick={() => void close(row)}>
            {`Close ${row.reference}`}
          </Button>
        ) : (
          "None"
        ),
    },
  ];

  const set = (patch: Partial<InstrumentDraft>) => setDraft((prev) => ({ ...prev, ...patch }));
  const text = (id: "reference" | "beneficiary" | "amount" | "linkedOrderRef", label: string, hint?: string, optional?: boolean) => (
    <FormField id={`in-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(c) => (
        <Input
          {...c}
          className={id === "beneficiary" ? undefined : styles.mono}
          inputMode={id === "amount" ? "decimal" : undefined}
          value={draft[id]}
          onChange={(e) => set({ [id]: e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Facility ${f.code}`}
        description={f.bankName}
        meta={<StatusPill status={f.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to={`/treasury/facilities?legalEntityId=${f.legalEntityId}`}>
            All facilities
          </Link>
        }
      />
      <StatGroup label="Facility utilization">
        <StatTile label="Limit" value={money(f.limit, f.currency)} />
        <StatTile label="Utilized" value={money(f.utilized, f.currency)} />
        <StatTile label="Headroom" value={money(f.headroom, f.currency)} />
        <StatTile label="Expires" value={f.expiryDate} />
      </StatGroup>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      <Card title="Instruments" headingLevel={3}>
        {instruments.error ? (
          <LoadFailure title="Could not load the instruments" problem={instruments.error} retry={instruments.reload} />
        ) : (
          <DataTable
            label="Instruments"
            columns={instrumentColumns}
            rows={instruments.data ?? []}
            getRowKey={(row) => row.id}
            loading={instruments.loading}
            empty={{ title: "No instruments booked", description: "Book a letter of credit or guarantee below." }}
          />
        )}
      </Card>
      {f.status === "active" ? (
        <Card title="Book an instrument" headingLevel={3}>
          <form className={styles.form} onSubmit={(e) => void book(e, f)} noValidate>
            <FormField id="in-kind" label="Kind">
              {(c) => (
                <Select {...c} value={draft.kind} onChange={(e) => set({ kind: e.target.value as InstrumentKind })}>
                  {INSTRUMENT_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {KIND_LABELS[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {text("reference", "Reference", "The bank's LC or guarantee number.")}
            {text("beneficiary", "Beneficiary")}
            {text("amount", `Amount (${f.currency})`, `Headroom is ${money(f.headroom, f.currency)}.`)}
            <FormField id="in-issue" label="Issue date" error={errors.issueDate}>
              {(c) => <Input {...c} type="date" value={draft.issueDate} onChange={(e) => set({ issueDate: e.target.value })} invalid={Boolean(errors.issueDate)} />}
            </FormField>
            <FormField id="in-expiry" label="Expiry date" error={errors.expiryDate}>
              {(c) => <Input {...c} type="date" value={draft.expiryDate} onChange={(e) => set({ expiryDate: e.target.value })} invalid={Boolean(errors.expiryDate)} />}
            </FormField>
            {text("linkedOrderRef", "Purchase or sales order", "The order the instrument secures.", true)}
            <FormField id="in-override" label="Above the limit" hint="Only a treasurer with the override duty may book past the limit.">
              {(c) => (
                <Select {...c} value={draft.overrideLimit ? "yes" : "no"} onChange={(e) => set({ overrideLimit: e.target.value === "yes" })}>
                  <option value="no">Refuse if the limit would be exceeded</option>
                  <option value="yes">Override the limit</option>
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "book"}>
                Book instrument
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Currency" mono>
            {f.currency}
          </Detail>
          <Detail term="Legal entity" mono>
            {f.legalEntityId}
          </Detail>
          <Detail term="Version" mono>
            {String(f.version)}
          </Detail>
        </dl>
      </Card>
    </div>
  );
}
