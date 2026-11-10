import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  changeAutoCharge,
  changeChargeCode,
  createAutoCharge,
  createChargeCode,
  describeProblem,
  getAutoCharge,
  getChargeCode,
  listAutoCharges,
  listChargeCodes,
  newIdempotencyKey,
  type AllocationRule,
  type AutoCharge,
  type AutoChargeInput,
  type AutoChargeItemKey,
  type AutoChargeKey,
  type ChargeCalc,
  type ChargeCode,
  type ChargeCodeInput,
  type ChargeSide,
  type ChargeStatus,
} from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, currencyPattern, datePattern, decimalPattern, statusLabel, todayIso, useResource, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const CALCS: { value: ChargeCalc; label: string }[] = [
  { value: "FIXED", label: "Fixed amount" },
  { value: "PER_UNIT", label: "Per unit" },
  { value: "PERCENT", label: "Percent of lines" },
];
const DEBITS: ChargeSide[] = ["ITEM", "LEDGER", "VENDOR"];
const CREDITS: ("LEDGER" | "VENDOR")[] = ["VENDOR", "LEDGER"];
const RULES: AllocationRule[] = ["AMOUNT", "QTY", "LINES"];
const STATUSES: ChargeStatus[] = ["ACTIVE", "INACTIVE"];
const KEYS: { value: AutoChargeKey; label: string }[] = [
  { value: "VENDOR", label: "One vendor" },
  { value: "GROUP", label: "A vendor group" },
  { value: "ALL", label: "Every vendor" },
];

const ITEM_KEYS: { value: AutoChargeItemKey; label: string }[] = [
  { value: "ALL", label: "The whole order (header charge)" },
  { value: "PRODUCT", label: "Lines of one product (line charge)" },
  { value: "GROUP", label: "Lines of a product group (line charge)" },
];

const calcLabel = (calc: ChargeCalc, value: string) =>
  calc === "PERCENT" ? `${value}% of lines` : calc === "PER_UNIT" ? `${value} per unit` : `${value} fixed`;

const codeColumns: DataTableColumn<ChargeCode>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "calc", header: "Default", render: (c) => calcLabel(c.calc, c.defaultValue) },
  { key: "debitType", header: "Debit", render: (c) => statusLabel(c.debitType) },
  { key: "creditType", header: "Credit", render: (c) => statusLabel(c.creditType) },
  { key: "allocationRule", header: "Allocated by", render: (c) => statusLabel(c.allocationRule) },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
];

/** Charge codes with status and text filters and load-more paging (FR-17.9). */
export function ChargeCodesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ChargeStatus | "">("");
  const [q, setQ] = useState("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<ChargeCode[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listChargeCodes({ status, q }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, q, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Charge codes"
        description="Kinds of charge such as freight, handling and customs: how each is calculated, where it is debited and credited, and how it is spread over the lines at invoice time. A purchase order copies the sides when a charge is entered, so changing a code does not move an order already placed."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/auto-charges">
              Auto charges
            </Link>
            <Link className={buttonClassName("primary")} to="/purchasing/charge-codes/new">
              New charge code
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="cc-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as ChargeStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="cc-text-filter" label="Code or name" optional>
            {(control) => (
              <Input
                {...control}
                value={q}
                onChange={(e) => {
                  setCursor(undefined);
                  setQ(e.target.value);
                }}
              />
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="charge codes" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Charge codes"
        columns={codeColumns}
        rows={page.error ? [] : rows}
        getRowKey={(c) => c.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No charge codes yet", description: "Create FREIGHT, HANDLING or CUSTOMS so buyers can pick them on a purchase order." }}
        onRowOpen={(c) => navigate(`/purchasing/charge-codes/${c.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface CodeForm {
  legalEntityId: string;
  code: string;
  name: string;
  calc: ChargeCalc;
  defaultValue: string;
  debitType: ChargeSide;
  creditType: "LEDGER" | "VENDOR";
  debitAccountId: string;
  creditAccountId: string;
  vendorId: string;
  allocationRule: AllocationRule;
  status: ChargeStatus;
}

const blankCode: CodeForm = {
  legalEntityId: "",
  code: "",
  name: "",
  calc: "FIXED",
  defaultValue: "0",
  debitType: "ITEM",
  creditType: "VENDOR",
  debitAccountId: "",
  creditAccountId: "",
  vendorId: "",
  allocationRule: "AMOUNT",
  status: "ACTIVE",
};

const hasVendorSide = (f: CodeForm) => f.debitType === "VENDOR" || f.creditType === "VENDOR";

export function validateChargeCode(f: CodeForm, creating: boolean): Errors {
  const e: Errors = {};
  if (creating) {
    if (!uuidPattern.test(f.legalEntityId.trim())) e.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!/^[A-Za-z0-9_-]{1,20}$/.test(f.code.trim())) e.code = "Use 1 to 20 letters, digits, _ or -.";
  }
  if (f.name.trim() === "") e.name = "Enter the name.";
  if (!decimalPattern.test(f.defaultValue.trim())) e.defaultValue = "Enter a decimal value.";
  else if (f.calc === "PERCENT" && Number(f.defaultValue) > 100) e.defaultValue = "A percentage is at most 100.";
  if (f.debitType === "LEDGER" && !uuidPattern.test(f.debitAccountId.trim())) e.debitAccountId = "Enter the debit account id (a UUID).";
  if (f.creditType === "LEDGER" && !uuidPattern.test(f.creditAccountId.trim())) e.creditAccountId = "Enter the credit account id (a UUID).";
  if (f.debitType === "VENDOR" && f.creditType === "VENDOR") e.creditType = "A vendor debit needs a ledger credit.";
  if (f.debitType === "VENDOR" && !uuidPattern.test(f.vendorId.trim())) e.vendorId = "Enter the vendor id (a UUID).";
  else if (hasVendorSide(f) && f.vendorId.trim() !== "" && !uuidPattern.test(f.vendorId.trim())) e.vendorId = "Enter a UUID or leave blank.";
  return e;
}

function toCodeInput(f: CodeForm): ChargeCodeInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId: f.legalEntityId.trim(),
    code: f.code.trim().toUpperCase(),
    name: f.name.trim(),
    calc: f.calc,
    defaultValue: f.defaultValue.trim(),
    debitType: f.debitType,
    creditType: f.creditType,
    debitAccountId: f.debitType === "LEDGER" ? opt(f.debitAccountId) : undefined,
    creditAccountId: f.creditType === "LEDGER" ? opt(f.creditAccountId) : undefined,
    // The service refuses a vendor without a vendor side and an account without a ledger side.
    vendorId: hasVendorSide(f) ? opt(f.vendorId) : undefined,
    allocationRule: f.allocationRule,
    status: f.status,
  };
}

/** The editable fields of a charge code, shared by the create form and the detail page. */
function CodeFields({ id, form, set, errors, creating }: { id: string; form: CodeForm; set: (patch: Partial<CodeForm>) => void; errors: Errors; creating: boolean }) {
  return (
    <div className={styles.grid}>
      {creating ? (
        <>
          <FormField id={`${id}-legal-entity`} label="Legal entity id" error={errors.legalEntityId}>
            {(c) => <Input {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id={`${id}-code`} label="Code" hint="Upper case, for example FREIGHT" error={errors.code}>
            {(c) => <Input {...c} value={form.code} maxLength={20} onChange={(e) => set({ code: e.target.value.toUpperCase() })} />}
          </FormField>
        </>
      ) : null}
      <FormField id={`${id}-name`} label="Name" error={errors.name}>
        {(c) => <Input {...c} value={form.name} onChange={(e) => set({ name: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-calc`} label="Calculation">
        {(c) => (
          <Select {...c} value={form.calc} onChange={(e) => set({ calc: e.target.value as ChargeCalc })}>
            {CALCS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id={`${id}-default`} label="Default value" hint="A purchase order may override it" error={errors.defaultValue}>
        {(c) => <Input {...c} inputMode="decimal" value={form.defaultValue} onChange={(e) => set({ defaultValue: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-debit`} label="Debit side" hint="Item capitalizes into inventory cost">
        {(c) => (
          <Select {...c} value={form.debitType} onChange={(e) => set({ debitType: e.target.value as ChargeSide })}>
            {DEBITS.map((s) => (
              <option key={s} value={s}>
                {statusLabel(s)}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {form.debitType === "LEDGER" ? (
        <FormField id={`${id}-debit-account`} label="Debit account id" error={errors.debitAccountId}>
          {(c) => <Input {...c} value={form.debitAccountId} onChange={(e) => set({ debitAccountId: e.target.value })} />}
        </FormField>
      ) : null}
      <FormField id={`${id}-credit`} label="Credit side" hint="Vendor is payable to the vendor below, or the order's vendor" error={errors.creditType}>
        {(c) => (
          <Select {...c} value={form.creditType} onChange={(e) => set({ creditType: e.target.value as "LEDGER" | "VENDOR" })}>
            {CREDITS.map((s) => (
              <option key={s} value={s}>
                {statusLabel(s)}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {form.creditType === "LEDGER" ? (
        <FormField id={`${id}-credit-account`} label="Credit account id" error={errors.creditAccountId}>
          {(c) => <Input {...c} value={form.creditAccountId} onChange={(e) => set({ creditAccountId: e.target.value })} />}
        </FormField>
      ) : null}
      {hasVendorSide(form) ? (
        <FormField
          id={`${id}-vendor`}
          label="Vendor id"
          optional={form.debitType !== "VENDOR"}
          hint={form.debitType === "VENDOR" ? "The vendor the charge is passed on to" : "Blank is the order's vendor"}
          error={errors.vendorId}
        >
          {(c) => <Input {...c} value={form.vendorId} onChange={(e) => set({ vendorId: e.target.value })} />}
        </FormField>
      ) : null}
      <FormField id={`${id}-allocation`} label="Allocated to lines by" hint="Applied when the vendor invoice is posted">
        {(c) => (
          <Select {...c} value={form.allocationRule} onChange={(e) => set({ allocationRule: e.target.value as AllocationRule })}>
            {RULES.map((r) => (
              <option key={r} value={r}>
                {r === "QTY" ? "Quantity" : r === "LINES" ? "Line count" : "Amount"}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {creating ? null : (
        <FormField id={`${id}-status`} label="Status" hint="An inactive code cannot be added to a new charge">
          {(c) => (
            <Select {...c} value={form.status} onChange={(e) => set({ status: e.target.value as ChargeStatus })}>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      )}
    </div>
  );
}

/** Create form for a charge code. */
export function ChargeCodeCreatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<CodeForm>(blankCode);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form]);
  const set = (patch: Partial<CodeForm>) => setForm((f) => ({ ...f, ...patch }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateChargeCode(form, true);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createChargeCode(toCodeInput(form), key);
      navigate(`/purchasing/charge-codes/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New charge code" description="A code is unique within its legal entity and cannot be renamed; deactivate it instead of deleting it." />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Charge code">
          <CodeFields id="cc" form={form} set={set} errors={errors} creating />
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save charge code
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/charge-codes">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const formOf = (c: ChargeCode): CodeForm => ({
  legalEntityId: c.legalEntityId,
  code: c.code,
  name: c.name,
  calc: c.calc,
  defaultValue: c.defaultValue,
  debitType: c.debitType,
  creditType: c.creditType,
  debitAccountId: c.debitAccountId ?? "",
  creditAccountId: c.creditAccountId ?? "",
  vendorId: c.vendorId ?? "",
  allocationRule: c.allocationRule,
  status: c.status,
});

/** One charge code: read it, change it under its version, and see the auto charges that use it. */
export function ChargeCodeDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getChargeCode(id, signal)).body, [id]);
  const autos = useResource(async (signal) => (await listAutoCharges({ chargeCodeId: id }, { signal })).items, [id]);
  const [saved, setSaved] = useState<ChargeCode | null>(null);
  const [draft, setDraft] = useState<CodeForm>(blankCode);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (resource.data) {
      setSaved(resource.data);
      setDraft(formOf(resource.data));
    }
  }, [resource.data]);

  function reload() {
    setFailure(null);
    setNotice(null);
    resource.reload();
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!saved) return;
    const found = validateChargeCode(draft, false);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await changeChargeCode(saved.id, toCodeInput(draft), saved.version);
      setSaved(body);
      setDraft(formOf(body));
      setNotice(`Charge code saved as version ${body.version}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-CHG-002";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={saved ? `Charge code ${saved.code}` : "Charge code"}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/charge-codes">
              All charge codes
            </Link>
            {saved ? (
              <Link className={buttonClassName("primary")} to={`/purchasing/auto-charges/new?chargeCodeId=${saved.id}`}>
                New auto charge
              </Link>
            ) : null}
          </>
        }
      />
      {resource.error ? <LoadError what="the charge code" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !saved ? <Skeleton /> : null}
      {saved ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Code", value: saved.code, mono: true },
                { label: "Legal entity", value: saved.legalEntityId, mono: true },
                { label: "Default", value: calcLabel(saved.calc, saved.defaultValue) },
                { label: "Status", value: <StatusBadge status={saved.status} /> },
                { label: "Version", value: String(saved.version), mono: true },
              ]}
            />
          </Card>
          <Card title="Change">
            <CodeFields id="ccd" form={draft} set={(patch) => setDraft((d) => ({ ...d, ...patch }))} errors={errors} creating={false} />
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={reload}>
                  Reload charge code
                </Button>
              ) : undefined
            }
          >
            {failure
              ? stale
                ? <ProblemMessage problem={failure} message="Someone else changed this charge code since you opened it. Reload to see their change, then save again." />
                : <ProblemMessage problem={failure} />
              : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      ) : null}
      {saved ? (
        <>
          {autos.error ? <LoadError what="the auto charges" error={autos.error} onRetry={autos.reload} /> : null}
          <DataTable
            label="Auto charges using this code"
            columns={autoColumns(new Map([[saved.id, saved]]))}
            rows={autos.error ? [] : (autos.data ?? [])}
            getRowKey={(a) => a.id}
            loading={autos.loading && autos.data === null}
            empty={{ title: "No auto charges", description: "Add one to put this charge on the orders of a vendor, a vendor group or every vendor." }}
          />
        </>
      ) : null}
    </div>
  );
}

const keyLabel = (a: AutoCharge) =>
  a.accountCode === "ALL" ? "Every vendor" : `${a.accountCode === "GROUP" ? "Vendor group" : "Vendor"} ${(a.accountId ?? "").slice(0, 8)}`;

const itemLabel = (a: AutoCharge) =>
  a.itemCode === "PRODUCT" ? `Product ${(a.itemId ?? "").slice(0, 8)}` : a.itemCode === "GROUP" ? `Product group ${(a.itemId ?? "").slice(0, 8)}` : "Whole order";

function autoColumns(codes: Map<string, ChargeCode>): DataTableColumn<AutoCharge>[] {
  return [
    {
      key: "chargeCodeId",
      header: "Charge code",
      mono: true,
      render: (a) => codes.get(a.chargeCodeId)?.code ?? <ShortId id={a.chargeCodeId} />,
    },
    { key: "accountCode", header: "Applies to", render: keyLabel },
    { key: "itemCode", header: "Charged on", render: itemLabel },
    {
      key: "value",
      header: "Value",
      render: (a) => (a.value !== undefined ? `${a.value}${a.currency ? ` ${a.currency}` : ""}` : "Code default"),
    },
    { key: "minOrderAmount", header: "Minimum order", render: (a) => (a.minOrderAmount ? `${a.minOrderAmount} ${a.currency ?? ""}` : "None") },
    { key: "validFrom", header: "Valid", mono: true, render: (a) => (a.validTo ? `${a.validFrom} to ${a.validTo}` : `From ${a.validFrom}`) },
    { key: "status", header: "Status", render: (a) => <StatusBadge status={a.status} /> },
  ];
}

/** Auto charges with a status filter and load-more paging (FR-17.9). */
export function AutoChargesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ChargeStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<AutoCharge[]>([]);
  const codes = useResource(async (signal) => (await listChargeCodes({}, { signal })).items, []);
  const page = useResource(
    async (signal) => {
      const result = await listAutoCharges({ status }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );
  const byId = useMemo(() => new Map((codes.data ?? []).map((c) => [c.id, c])), [codes.data]);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Auto charges"
        description="Charges that are added to a purchase order when it is saved with automatic charges: by vendor, vendor group or for every vendor, optionally above a minimum order amount."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/charge-codes">
              Charge codes
            </Link>
            <Link className={buttonClassName("primary")} to="/purchasing/auto-charges/new">
              New auto charge
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="ac-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as ChargeStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="auto charges" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Auto charges"
        columns={autoColumns(byId)}
        rows={page.error ? [] : rows}
        getRowKey={(a) => a.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No auto charges yet", description: "Add one to put a charge on matching orders without typing it." }}
        onRowOpen={(a) => navigate(`/purchasing/auto-charges/${a.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface AutoForm {
  legalEntityId: string;
  chargeCodeId: string;
  accountCode: AutoChargeKey;
  accountId: string;
  itemCode: AutoChargeItemKey;
  itemId: string;
  value: string;
  minOrderAmount: string;
  currency: string;
  validFrom: string;
  validTo: string;
  status: ChargeStatus;
}

function validateAuto(f: AutoForm, calc: ChargeCalc | undefined, creating: boolean): Errors {
  const e: Errors = {};
  if (creating) {
    if (!uuidPattern.test(f.legalEntityId.trim())) e.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!uuidPattern.test(f.chargeCodeId)) e.chargeCodeId = "Choose a charge code.";
    if (f.accountCode !== "ALL" && !uuidPattern.test(f.accountId.trim())) e.accountId = "Enter the vendor or vendor group id (a UUID).";
    if (f.itemCode !== "ALL" && !uuidPattern.test(f.itemId.trim())) e.itemId = "Enter the product or product group id (a UUID).";
  }
  if (f.value.trim() !== "" && !decimalPattern.test(f.value.trim())) e.value = "Enter a decimal value.";
  else if (calc === "PERCENT" && Number(f.value) > 100) e.value = "A percentage is at most 100.";
  if (f.minOrderAmount.trim() !== "" && (!decimalPattern.test(f.minOrderAmount.trim()) || Number(f.minOrderAmount) <= 0)) {
    e.minOrderAmount = "Enter an amount above zero.";
  }
  const needsCurrency = f.minOrderAmount.trim() !== "" || (calc !== undefined && calc !== "PERCENT");
  if (f.currency.trim() === "" ? needsCurrency : !currencyPattern.test(f.currency.trim())) {
    e.currency = needsCurrency && f.currency.trim() === "" ? "Enter the currency; this charge is an amount of money." : "Enter a 3-letter currency code.";
  }
  if (!datePattern.test(f.validFrom)) e.validFrom = "Enter a date.";
  if (f.validTo !== "" && (!datePattern.test(f.validTo) || f.validTo < f.validFrom)) e.validTo = "Enter a date on or after valid from.";
  return e;
}

function toAutoInput(f: AutoForm): AutoChargeInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId: f.legalEntityId.trim(),
    chargeCodeId: f.chargeCodeId,
    accountCode: f.accountCode,
    accountId: f.accountCode === "ALL" ? undefined : opt(f.accountId),
    itemCode: f.itemCode,
    itemId: f.itemCode === "ALL" ? undefined : opt(f.itemId),
    value: opt(f.value),
    minOrderAmount: opt(f.minOrderAmount),
    currency: opt(f.currency),
    validFrom: f.validFrom,
    validTo: opt(f.validTo),
    status: f.status,
  };
}

/** Value, minimum, currency, window and status: what an auto charge lets you change. */
function AutoWindowFields({ id, form, set, errors }: { id: string; form: AutoForm; set: (patch: Partial<AutoForm>) => void; errors: Errors }) {
  return (
    <>
      <FormField id={`${id}-value`} label="Value" optional hint="Blank uses the charge code's default" error={errors.value}>
        {(c) => <Input {...c} inputMode="decimal" value={form.value} onChange={(e) => set({ value: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-min`} label="Minimum order amount" optional hint="Lines total after discount" error={errors.minOrderAmount}>
        {(c) => <Input {...c} inputMode="decimal" value={form.minOrderAmount} onChange={(e) => set({ minOrderAmount: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-currency`} label="Currency" optional hint="Required for an amount of money" error={errors.currency}>
        {(c) => <Input {...c} value={form.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />}
      </FormField>
      <FormField id={`${id}-from`} label="Valid from" error={errors.validFrom}>
        {(c) => <Input {...c} type="date" value={form.validFrom} onChange={(e) => set({ validFrom: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-to`} label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
        {(c) => <Input {...c} type="date" value={form.validTo} onChange={(e) => set({ validTo: e.target.value })} />}
      </FormField>
    </>
  );
}

/** Create form for an auto charge; ?chargeCodeId= preselects the code. */
export function AutoChargeCreatePage() {
  const navigate = useNavigate();
  const [search] = useSearchParams();
  const preselected = search.get("chargeCodeId") ?? "";
  const [form, setForm] = useState<AutoForm>({
    legalEntityId: "",
    chargeCodeId: preselected,
    accountCode: "VENDOR",
    accountId: "",
    itemCode: "ALL",
    itemId: "",
    value: "",
    minOrderAmount: "",
    currency: "",
    validFrom: todayIso(),
    validTo: "",
    status: "ACTIVE",
  });
  const codes = useResource(async (signal) => (await listChargeCodes({ status: "ACTIVE" }, { signal })).items, []);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form]);
  const set = (patch: Partial<AutoForm>) => setForm((f) => ({ ...f, ...patch }));
  const available = (codes.data ?? []).filter((c) => !uuidPattern.test(form.legalEntityId.trim()) || c.legalEntityId === form.legalEntityId.trim());
  const chosen = (codes.data ?? []).find((c) => c.id === form.chargeCodeId);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateAuto(form, chosen?.calc, true);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createAutoCharge(toAutoInput(form), key);
      navigate(`/purchasing/auto-charges/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New auto charge" description="The charge is added to a purchase order saved with automatic charges when the vendor, currency and order total match." />
      {codes.error ? <LoadError what="the charge codes" error={codes.error} onRetry={codes.reload} /> : null}
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Auto charge">
          <div className={styles.grid}>
            <FormField id="ac-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
              {(c) => <Input {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="ac-code" label="Charge code" error={errors.chargeCodeId}>
              {(c) => (
                <Select {...c} value={form.chargeCodeId} onChange={(e) => set({ chargeCodeId: e.target.value })}>
                  <option value="">Choose a charge code</option>
                  {available.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.code} - {o.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="ac-key" label="Applies to">
              {(c) => (
                <Select {...c} value={form.accountCode} onChange={(e) => set({ accountCode: e.target.value as AutoChargeKey })}>
                  {KEYS.map((k) => (
                    <option key={k.value} value={k.value}>
                      {k.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {form.accountCode !== "ALL" ? (
              <FormField id="ac-account" label={form.accountCode === "GROUP" ? "Vendor group id" : "Vendor id"} error={errors.accountId}>
                {(c) => <Input {...c} value={form.accountId} onChange={(e) => set({ accountId: e.target.value })} />}
              </FormField>
            ) : null}
            <FormField id="ac-item-key" label="Charged on" hint="A product or product group adds the charge to each matching line">
              {(c) => (
                <Select {...c} value={form.itemCode} onChange={(e) => set({ itemCode: e.target.value as AutoChargeItemKey })}>
                  {ITEM_KEYS.map((k) => (
                    <option key={k.value} value={k.value}>
                      {k.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {form.itemCode !== "ALL" ? (
              <FormField id="ac-item" label={form.itemCode === "GROUP" ? "Product group id" : "Product id"} error={errors.itemId}>
                {(c) => <Input {...c} value={form.itemId} onChange={(e) => set({ itemId: e.target.value })} />}
              </FormField>
            ) : null}
            <AutoWindowFields id="ac" form={form} set={set} errors={errors} />
          </div>
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save auto charge
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/auto-charges">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** One auto charge: read it and change value, minimum, currency, window and status under its version. */
export function AutoChargeDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getAutoCharge(id, signal)).body, [id]);
  const code = useResource(async (signal) => (resource.data ? (await getChargeCode(resource.data.chargeCodeId, signal)).body : null), [resource.data?.chargeCodeId]);
  const [saved, setSaved] = useState<AutoCharge | null>(null);
  const [draft, setDraft] = useState<AutoForm | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const formOfAuto = (a: AutoCharge): AutoForm => ({
    legalEntityId: a.legalEntityId,
    chargeCodeId: a.chargeCodeId,
    accountCode: a.accountCode,
    accountId: a.accountId ?? "",
    itemCode: a.itemCode ?? "ALL",
    itemId: a.itemId ?? "",
    value: a.value ?? "",
    minOrderAmount: a.minOrderAmount ?? "",
    currency: a.currency ?? "",
    validFrom: a.validFrom,
    validTo: a.validTo ?? "",
    status: a.status,
  });

  useEffect(() => {
    if (resource.data) {
      setSaved(resource.data);
      setDraft(formOfAuto(resource.data));
    }
  }, [resource.data]);

  function reload() {
    setFailure(null);
    setNotice(null);
    resource.reload();
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!saved || !draft) return;
    const found = validateAuto(draft, code.data?.calc, false);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await changeAutoCharge(saved.id, toAutoInput(draft), saved.version);
      setSaved(body);
      setDraft(formOfAuto(body));
      setNotice(`Auto charge saved as version ${body.version}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-CHG-003";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Auto charge"
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/auto-charges">
            All auto charges
          </Link>
        }
      />
      {resource.error ? <LoadError what="the auto charge" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !saved ? <Skeleton /> : null}
      {saved && draft ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Charge code", value: code.data ? `${code.data.code} - ${code.data.name}` : saved.chargeCodeId, mono: !code.data },
                { label: "Applies to", value: keyLabel(saved) },
                { label: "Charged on", value: itemLabel(saved) },
                { label: "Legal entity", value: saved.legalEntityId, mono: true },
                { label: "Status", value: <StatusBadge status={saved.status} /> },
                { label: "Version", value: String(saved.version), mono: true },
              ]}
            />
          </Card>
          <Card title="Change">
            <div className={styles.grid}>
              <AutoWindowFields id="acd" form={draft} set={(patch) => setDraft((d) => (d ? { ...d, ...patch } : d))} errors={errors} />
              <FormField id="acd-status" label="Status">
                {(c) => (
                  <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ChargeStatus })}>
                    {STATUSES.map((s) => (
                      <option key={s} value={s}>
                        {statusLabel(s)}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </div>
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={reload}>
                  Reload auto charge
                </Button>
              ) : undefined
            }
          >
            {failure
              ? stale
                ? <ProblemMessage problem={failure} message="Someone else changed this auto charge since you opened it. Reload to see their change, then save again." />
                : <ProblemMessage problem={failure} />
              : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      ) : null}
    </div>
  );
}
