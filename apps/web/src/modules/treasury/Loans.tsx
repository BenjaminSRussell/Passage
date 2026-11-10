import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
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
  createLoan,
  DAY_COUNTS,
  describeProblem,
  getLoan,
  listLoans,
  newKey,
  PERIOD_MONTHS,
  SCHEDULE_TYPES,
  type DayCount,
  type Loan,
  type LoanInput,
  type PeriodMonths,
  type Problem,
  type ScheduleLine,
  type ScheduleType,
} from "./api/client";
import { CURRENCY, DAY, fractionFromPercent, money, percent, PERCENT, UUID, validAmount } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

export const PERIOD_LABELS: Record<PeriodMonths, string> = { 1: "Monthly", 3: "Quarterly", 6: "Semi-annual", 12: "Annual" };
const SCHEDULE_HINTS: Record<ScheduleType, string> = {
  Annuity: "Equal instalments of interest and principal.",
  Linear: "Equal principal repayments; interest falls with the balance.",
  Bullet: "Interest each period; all principal repaid at maturity.",
};

const loanColumns: DataTableColumn<Loan>[] = [
  { key: "counterpartyName", header: "Counterparty" },
  { key: "role", header: "Role", render: (row) => (row.role === "lender" ? "Lender" : "Borrower") },
  { key: "principal", header: "Principal", mono: true, align: "end", render: (row) => money(row.principal, row.currency) },
  { key: "fixedRate", header: "Rate", mono: true, align: "end", render: (row) => percent(row.fixedRate) },
  { key: "scheduleType", header: "Schedule" },
  { key: "startDate", header: "Start", mono: true },
  { key: "maturityDate", header: "Maturity", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Loan contracts of one legal entity (FR-36.19). */
export function LoansPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const [draft, setDraft] = useState(legalEntityId);
  const [error, setError] = useState<string | undefined>();
  const chosen = UUID.test(legalEntityId);
  const state = useLoad((signal) => (chosen ? listLoans(legalEntityId, signal) : Promise.resolve([])), legalEntityId);

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

  const newLink = chosen ? `/treasury/loans/new?legalEntityId=${encodeURIComponent(legalEntityId)}` : "/treasury/loans/new";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Loans"
        description="Bank and intercompany loans the legal entity has borrowed or lent, with their repayment and interest schedules."
        actions={
          <Link className={buttonClassName("primary")} to={newLink}>
            New loan
          </Link>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={choose} noValidate>
          <FormField id="ln-entity" label="Legal entity" error={error}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button type="submit">Show loans</Button>
        </form>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="Loans belong to one legal entity. Enter its id to list them." />
        </Card>
      ) : state.error ? (
        <LoadFailure title="Could not load the loans" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Loans"
          columns={loanColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No loans yet", description: "Record the legal entity's first loan contract." }}
          onRowOpen={(row) => navigate(`/treasury/loans/${row.id}`)}
        />
      )}
    </div>
  );
}

export interface LoanDraft {
  legalEntityId: string;
  role: "borrower" | "lender";
  counterpartyName: string;
  principal: string;
  currency: string;
  startDate: string;
  maturityDate: string;
  scheduleType: ScheduleType;
  ratePercent: string;
  dayCount: DayCount;
  periodMonths: PeriodMonths;
}

export function validateLoan(v: LoanDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  const name = v.counterpartyName.trim();
  if (name.length < 1 || name.length > 140) errors.counterpartyName = "Enter the bank or group company, up to 140 characters.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter an ISO 4217 code such as EUR.";
  if (!validAmount(v.principal, v.currency)) errors.principal = "Enter a positive principal with no more decimals than the currency allows.";
  if (!DAY.test(v.startDate)) errors.startDate = "Choose the start date.";
  if (!DAY.test(v.maturityDate)) errors.maturityDate = "Choose the maturity date.";
  else if (DAY.test(v.startDate) && v.maturityDate <= v.startDate) errors.maturityDate = "The maturity date must be after the start date.";
  const rate = v.ratePercent.trim();
  const [whole = "", frac = ""] = rate.split(".");
  const above100 = Number.parseInt(whole, 10) > 100 || (Number.parseInt(whole, 10) === 100 && /[1-9]/.test(frac));
  if (!PERCENT.test(rate) || above100) {
    errors.ratePercent = "Enter the annual rate in percent, 0 to 100 with up to four decimals, such as 3.25.";
  }
  return errors;
}

export function toLoanInput(v: LoanDraft): LoanInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    role: v.role,
    counterpartyName: v.counterpartyName.trim(),
    principal: v.principal.trim(),
    currency: v.currency.trim(),
    startDate: v.startDate,
    maturityDate: v.maturityDate,
    scheduleType: v.scheduleType,
    fixedRate: fractionFromPercent(v.ratePercent),
    dayCount: v.dayCount,
    periodMonths: v.periodMonths,
  };
}

/** Record a fixed-rate loan; the service generates the schedule. A retry reuses the Idempotency-Key. */
export function NewLoanPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<LoanDraft>({
    legalEntityId: params.get("legalEntityId") ?? "",
    role: "borrower",
    counterpartyName: "",
    principal: "",
    currency: "",
    startDate: "",
    maturityDate: "",
    scheduleType: "Annuity",
    ratePercent: "",
    dayCount: "ACT/360",
    periodMonths: 1,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const set = (patch: Partial<LoanDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLoan(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createLoan(toLoanInput(values), key.current);
      navigate(`/treasury/loans/${created.id}`);
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      const fields = { ...problem.fields };
      if (fields.fixedRate) fields.ratePercent = fields.fixedRate;
      if (Object.keys(fields).length > 0) setErrors(fields);
      else if (problem.code === "TRS-0446") setErrors({ maturityDate: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: "legalEntityId" | "counterpartyName" | "principal" | "currency" | "ratePercent", label: string, hint?: string) => (
    <FormField id={`ln-${id}`} label={label} hint={hint} error={errors[id]}>
      {(c) => (
        <Input
          {...c}
          className={id === "counterpartyName" ? undefined : styles.mono}
          inputMode={id === "principal" || id === "ratePercent" ? "decimal" : undefined}
          value={values[id]}
          onChange={(e) => set({ [id]: e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New loan"
        description="A fixed-rate loan. The maturity must fall a whole number of periods after the start; month ends stay month ends."
        actions={
          <Link className={buttonClassName("secondary")} to="/treasury/loans">
            Loans
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("legalEntityId", "Legal entity id")}
          <FormField id="ln-role" label="Role">
            {(c) => (
              <Select {...c} value={values.role} onChange={(e) => set({ role: e.target.value as LoanDraft["role"] })}>
                <option value="borrower">Borrower</option>
                <option value="lender">Lender</option>
              </Select>
            )}
          </FormField>
          {text("counterpartyName", "Counterparty", "The lending bank, or the group company for an intercompany loan.")}
          {text("principal", "Principal", "For example 1000000.00.")}
          {text("currency", "Currency", "ISO 4217, for example EUR.")}
          {text("ratePercent", "Annual rate (%)", "Fixed, for example 3.00.")}
          <FormField id="ln-start" label="Start date" error={errors.startDate}>
            {(c) => <Input {...c} type="date" value={values.startDate} onChange={(e) => set({ startDate: e.target.value })} invalid={Boolean(errors.startDate)} />}
          </FormField>
          <FormField id="ln-maturity" label="Maturity date" error={errors.maturityDate}>
            {(c) => <Input {...c} type="date" value={values.maturityDate} onChange={(e) => set({ maturityDate: e.target.value })} invalid={Boolean(errors.maturityDate)} />}
          </FormField>
          <FormField id="ln-schedule" label="Repayment" hint={SCHEDULE_HINTS[values.scheduleType]}>
            {(c) => (
              <Select {...c} value={values.scheduleType} onChange={(e) => set({ scheduleType: e.target.value as ScheduleType })}>
                {SCHEDULE_TYPES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ln-period" label="Period">
            {(c) => (
              <Select {...c} value={String(values.periodMonths)} onChange={(e) => set({ periodMonths: Number(e.target.value) as PeriodMonths })}>
                {PERIOD_MONTHS.map((m) => (
                  <option key={m} value={String(m)}>
                    {PERIOD_LABELS[m]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ln-daycount" label="Day count">
            {(c) => (
              <Select {...c} value={values.dayCount} onChange={(e) => set({ dayCount: e.target.value as DayCount })}>
                {DAY_COUNTS.map((d) => (
                  <option key={d} value={d}>
                    {d}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not create the loan" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create loan and schedule
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

const scheduleColumns: DataTableColumn<ScheduleLine>[] = [
  { key: "periodNo", header: "Period", mono: true, align: "end", render: (row) => String(row.periodNo) },
  { key: "periodStart", header: "From", mono: true },
  { key: "periodEnd", header: "To", mono: true },
  { key: "days", header: "Days", mono: true, align: "end", render: (row) => String(row.days) },
  { key: "openingPrincipal", header: "Opening", mono: true, align: "end" },
  { key: "interest", header: "Interest", mono: true, align: "end" },
  { key: "principalRepayment", header: "Repayment", mono: true, align: "end" },
  { key: "closingPrincipal", header: "Closing", mono: true, align: "end" },
];

/** One loan contract with its schedule. */
export function LoanDetailPage() {
  const { id = "" } = useParams();
  const loan = useLoad((signal) => getLoan(id, signal), `loan-${id}`);

  if (loan.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Loan" />
        <LoadFailure title="Could not load the loan" problem={loan.error} retry={loan.reload} />
      </div>
    );
  }
  const l = loan.data;
  if (!l) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Loan" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const schedule = l.schedule ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Loan with ${l.counterpartyName}`}
        description={`${l.role === "lender" ? "Lent" : "Borrowed"} ${money(l.principal, l.currency)} at ${percent(l.fixedRate)} ${l.dayCount}, ${l.scheduleType.toLowerCase()} repayment.`}
        meta={<StatusPill status={l.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to={`/treasury/loans?legalEntityId=${l.legalEntityId}`}>
            All loans
          </Link>
        }
      />
      <StatGroup label="Loan terms">
        <StatTile label="Principal" value={money(l.principal, l.currency)} />
        <StatTile label="Rate" value={percent(l.fixedRate)} />
        <StatTile label="Periods" value={String(schedule.length)} hint={PERIOD_LABELS[l.periodMonths]} />
        <StatTile label="Maturity" value={l.maturityDate} />
      </StatGroup>
      <Card title="Schedule" headingLevel={3}>
        <DataTable
          label="Loan schedule"
          columns={scheduleColumns}
          rows={schedule}
          getRowKey={(row) => String(row.periodNo)}
          empty={{ title: "No schedule", description: "The service returned this loan without schedule lines." }}
        />
      </Card>
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Start" mono>
            {l.startDate}
          </Detail>
          <Detail term="Day count" mono>
            {l.dayCount}
          </Detail>
          <Detail term="Legal entity" mono>
            {l.legalEntityId}
          </Detail>
          <Detail term="Version" mono>
            {String(l.version)}
          </Detail>
        </dl>
      </Card>
    </div>
  );
}
