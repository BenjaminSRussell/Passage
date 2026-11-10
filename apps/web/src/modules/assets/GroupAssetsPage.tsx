import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  CONVENTIONS,
  createGroupAsset,
  describeProblem,
  getGroupAsset,
  listAssets,
  listGroupAssets,
  METHODS,
  newKey,
  type Asset,
  type GroupAsset,
  type GroupMember,
  type GroupYear,
  type Method,
  type NewGroupAsset,
  type Page,
  type Problem,
} from "./api/client";
import { StatusBadge } from "./badges";
import { DECIMAL, money, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;

const listColumns: DataTableColumn<GroupAsset>[] = [
  { key: "groupCode", header: "Group", mono: true },
  { key: "name", header: "Name" },
  { key: "classCode", header: "Class", mono: true },
  { key: "bookCode", header: "Book", mono: true },
  { key: "taxYear", header: "Tax year", align: "end" },
  { key: "memberCount", header: "Members", align: "end" },
  { key: "basis", header: "Basis", align: "end", render: (g) => money(g.basis, g.currency) },
];

/** Group assets for pooled tax depreciation (FR-37.16), ordered by code. */
export function GroupAssetsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listGroupAssets(undefined, signal), "groups");
  const [more, setMore] = useState<GroupAsset[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listGroupAssets(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Group assets"
        description="Pools of assets of one class and tax year that a tax book depreciates together, such as a German Sammelposten, a UK capital allowance pool or a US general asset account."
        actions={
          <Link className={buttonClassName("primary")} to="/assets/group-assets/new">
            New group asset
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load group assets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Group assets"
          columns={listColumns}
          rows={[...(state.data?.items ?? []), ...more]}
          getRowKey={(g) => g.id}
          loading={state.loading}
          empty={{ title: "No group assets yet", description: "Create a group asset to pool assets on a tax book." }}
          onRowOpen={(g) => navigate(`/assets/group-assets/${g.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more group assets" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export interface GroupValues {
  legalEntityId: string;
  groupCode: string;
  name: string;
  classCode: string;
  bookCode: string;
  taxYear: string;
  method: Method;
  factor: string;
  switchToStraightLine: boolean;
  serviceLifePeriods: string;
  convention: string;
  memberAssetIds: string[];
}

type GroupErrors = Partial<Record<keyof GroupValues, string>>;

/** Checks that mirror the service (api createGroupAsset); member eligibility is checked by the service (AST-0031, AST-0535). */
export function validateGroup(values: GroupValues): { errors: GroupErrors; input?: NewGroupAsset } {
  const errors: GroupErrors = {};
  const le = values.legalEntityId.trim();
  if (!UUID.test(le)) errors.legalEntityId = "Enter the legal entity UUID.";
  const code = values.groupCode.trim();
  if (!CODE.test(code)) errors.groupCode = "Use upper-case letters, digits, hyphen or underscore, up to 20 characters.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const cls = values.classCode.trim();
  if (!CODE.test(cls)) errors.classCode = "Enter the asset class of the members.";
  const book = values.bookCode.trim();
  if (!CODE.test(book)) errors.bookCode = "Enter the tax book code, such as DE-TAX.";
  const year = /^\d{4}$/.test(values.taxYear.trim()) ? Number.parseInt(values.taxYear.trim(), 10) : NaN;
  if (!(year >= 1900 && year <= 2200)) errors.taxYear = "Enter a four-digit tax year.";
  const life = /^\d{1,4}$/.test(values.serviceLifePeriods.trim()) ? Number.parseInt(values.serviceLifePeriods.trim(), 10) : NaN;
  const maxLife = values.method === "StraightLine" ? 1200 : 100;
  if (!(life >= 1 && life <= maxLife)) errors.serviceLifePeriods = values.method === "StraightLine" ? "Enter 1 to 1200 months." : "Enter 1 to 100 years.";
  if (!CONVENTIONS[values.method].includes(values.convention)) errors.convention = "Choose a convention for this method.";
  const factor = values.factor.trim();
  if (values.method === "DecliningBalance" && (!DECIMAL.test(factor) || !/^(1(\.\d+)?|2(\.\d+)?|3(\.0+)?)$/.test(factor))) {
    errors.factor = "Enter a factor from 1 to 3, such as 2 for 200%.";
  }
  if (values.memberAssetIds.length < 1) errors.memberAssetIds = "Choose at least one member asset.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: le,
      groupCode: code,
      name,
      classCode: cls,
      bookCode: book,
      taxYear: year,
      method: values.method,
      ...(values.method === "DecliningBalance" ? { factor, switchToStraightLine: values.switchToStraightLine } : {}),
      serviceLifePeriods: life,
      convention: values.convention,
      memberAssetIds: values.memberAssetIds,
    },
  };
}

/** Candidate members: the Open assets of the legal entity, filtered to the group's class on the page. */
function MemberPicker({ legalEntityId, classCode, selected, onChange, error }: {
  legalEntityId: string;
  classCode: string;
  selected: string[];
  onChange(ids: string[]): void;
  error?: string;
}) {
  const valid = UUID.test(legalEntityId);
  const state = useLoad((signal) => (valid ? listAssets({ status: "Open", legalEntityId }, signal) : Promise.resolve<Page<Asset>>({ items: [] })), valid ? legalEntityId : "");
  const [more, setMore] = useState<Asset[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listAssets({ status: "Open", legalEntityId, after: cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  let body: ReactNode;
  if (!valid) {
    body = <p className={styles.note}>Enter the legal entity to choose its open assets.</p>;
  } else if (state.error) {
    body = (
      <Alert tone="danger" title="Could not load the assets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  } else if (state.loading && !state.data) {
    body = <Skeleton height="tile" />;
  } else {
    const rows = [...(state.data?.items ?? []), ...more].filter((a) => classCode.trim() === "" || a.classCode === classCode.trim());
    const all = rows.length > 0 && rows.every((a) => selected.includes(a.id));
    body =
      rows.length === 0 ? (
        <p className={styles.note}>{`No open asset${classCode.trim() ? ` of class ${classCode.trim()}` : ""} in this legal entity.`}</p>
      ) : (
        <>
          <label className={styles.check} htmlFor="group-members-all">
            <Input id="group-members-all" type="checkbox" checked={all} onChange={(e) => onChange(e.target.checked ? [...new Set([...selected, ...rows.map((a) => a.id)])] : selected.filter((x) => !rows.some((a) => a.id === x)))} />
            {`Select all ${rows.length} shown`}
          </label>
          {rows.map((a) => (
            <label key={a.id} className={styles.check} htmlFor={`group-member-${a.id}`}>
              <Input
                id={`group-member-${a.id}`}
                type="checkbox"
                checked={selected.includes(a.id)}
                onChange={(e) => onChange(e.target.checked ? [...selected, a.id] : selected.filter((x) => x !== a.id))}
              />
              {`${a.assetNumber} ${a.name} (${a.classCode})`}
            </label>
          ))}
          {moreError ? (
            <Alert tone="danger" title="Could not load more assets" live="alert">
              <ProblemMessage problem={moreError} />
            </Alert>
          ) : null}
          {cursor ? (
            <div>
              <Button size="sm" onClick={loadMore} loading={loadingMore}>
                Load more assets
              </Button>
            </div>
          ) : null}
        </>
      );
  }
  return (
    <fieldset className={styles.variant} aria-describedby={error ? "group-members-error" : undefined}>
      <legend className={styles.term}>{`Members (${selected.length} chosen)`}</legend>
      {body}
      {error ? (
        <span id="group-members-error" className={styles.error}>
          {error}
        </span>
      ) : null}
    </fieldset>
  );
}

/** Create a group asset on a tax book and assign its members (FR-37.16, duty assets.asset.configure). */
export function NewGroupAssetPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<GroupValues>(() => ({
    legalEntityId: "",
    groupCode: "",
    name: "",
    classCode: "",
    bookCode: "",
    taxYear: String(new Date().getUTCFullYear()),
    method: "StraightLine",
    factor: "2",
    switchToStraightLine: true,
    serviceLifePeriods: "60",
    convention: "None",
    memberAssetIds: [],
  }));
  const [errors, setErrors] = useState<GroupErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<GroupValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateGroup(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createGroupAsset(input, key.current);
      navigate(`/assets/group-assets/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as GroupErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (field: "legalEntityId" | "groupCode" | "name" | "classCode" | "bookCode" | "taxYear" | "serviceLifePeriods" | "factor", label: string, hint?: string) => (
    <FormField id={`group-${field}`} label={label} hint={hint} error={errors[field]}>
      {(control) => <Input {...control} value={values[field]} onChange={(e) => update({ [field]: e.target.value } as Partial<GroupValues>)} invalid={Boolean(errors[field])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New group asset"
        description="Every member must be an open asset of the class, placed in service in the tax year, with a book of the group's book code. That book is then depreciated on the group's total; the members' other books keep their own depreciation."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New group asset">
          {text("legalEntityId", "Legal entity")}
          {text("groupCode", "Group code", "Such as SP-2026.")}
          {text("name", "Name")}
          {text("classCode", "Asset class")}
          {text("bookCode", "Tax book", "The book code the group depreciates, such as DE-TAX.")}
          {text("taxYear", "Tax year")}
          <FormField id="group-method" label="Method">
            {(control) => (
              <Select {...control} value={values.method} onChange={(e) => update({ method: e.target.value as Method, convention: "None" })}>
                {METHODS.map((m) => (
                  <option key={m} value={m}>
                    {sentence(m)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("serviceLifePeriods", values.method === "StraightLine" ? "Life (months)" : "Life (years)", values.method === "StraightLine" ? "A Sammelposten is 60 months." : undefined)}
          <FormField id="group-convention" label="Convention" error={errors.convention}>
            {(control) => (
              <Select {...control} value={values.convention} onChange={(e) => update({ convention: e.target.value })}>
                {CONVENTIONS[values.method].map((c) => (
                  <option key={c} value={c}>
                    {sentence(c)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.method === "DecliningBalance" ? (
            <>
              {text("factor", "Factor", "2 for 200%.")}
              <div className={styles.formActions}>
                <label className={styles.check} htmlFor="group-switch">
                  <Input id="group-switch" type="checkbox" checked={values.switchToStraightLine} onChange={(e) => update({ switchToStraightLine: e.target.checked })} />
                  Switch to straight line
                </label>
              </div>
            </>
          ) : null}
          <MemberPicker
            legalEntityId={values.legalEntityId.trim()}
            classCode={values.classCode}
            selected={values.memberAssetIds}
            onChange={(ids) => update({ memberAssetIds: ids })}
            error={errors.memberAssetIds}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0535" ? "An asset is already in a group" : "Could not create the group asset"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create group asset
            </Button>
            <Link className={buttonClassName("ghost")} to="/assets/group-assets">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

function memberColumns(currency: string): DataTableColumn<GroupMember>[] {
  return [
    { key: "assetNumber", header: "Asset", mono: true, render: (m) => <Link to={`/assets/${m.assetId}`}>{m.assetNumber}</Link> },
    { key: "name", header: "Name" },
    { key: "inServiceDate", header: "In service", mono: true },
    { key: "cost", header: "Cost", align: "end", render: (m) => money(m.cost, currency) },
    { key: "bookStatus", header: "Book status", render: (m) => <StatusBadge status={m.bookStatus} /> },
  ];
}

function scheduleColumns(currency: string): DataTableColumn<GroupYear>[] {
  return [
    { key: "year", header: "Year", mono: true },
    { key: "amount", header: "Depreciation", align: "end", render: (y) => money(y.amount, currency) },
    { key: "accumulated", header: "Accumulated", align: "end", render: (y) => money(y.accumulated, currency) },
    { key: "nbv", header: "NBV", align: "end", render: (y) => money(y.nbv, currency) },
  ];
}

/** A group asset: its profile, basis, members and the projected yearly depreciation on the basis (AC-37.10). */
export function GroupAssetDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getGroupAsset(id, signal), id);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Group asset" />
        <Alert tone="danger" title="Could not load the group asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/group-assets">
          Back to group assets
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Group asset" />
        <span className="visually-hidden">Loading the group asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const g = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={`${g.groupCode} ${g.name}`} meta={<StatusBadge status={g.status} />} />
      <Card title="Group asset">
        <dl className={styles.details}>
          <Detail term="Tax book">
            <span className={styles.mono}>{g.bookCode}</span>
          </Detail>
          <Detail term="Class">
            <span className={styles.mono}>{g.classCode}</span>
          </Detail>
          <Detail term="Tax year">{g.taxYear}</Detail>
          <Detail term="Profile">
            {`${sentence(g.method)}${g.method === "DecliningBalance" ? ` ${g.factor}×` : ""}, ${g.serviceLifePeriods} ${g.method === "StraightLine" ? "months" : "years"}, ${sentence(g.convention)}`}
          </Detail>
          <Detail term="Basis">{money(g.basis, g.currency)}</Detail>
          <Detail term="Members">{g.memberCount}</Detail>
        </dl>
      </Card>
      <Alert tone="info" title="Members take no own charge" live="status">
        {`A depreciation run of ${g.bookCode} lists each member book as an exception (AST-0035) with no charge. Posting the group's own charge is not built yet.`}
      </Alert>
      <Card title="Projected group depreciation">
        <DataTable
          label="Group schedule"
          columns={scheduleColumns(g.currency)}
          rows={g.schedule ?? []}
          getRowKey={(y) => y.year}
          empty={{ title: "Nothing to depreciate", description: "The members have no cost left on this book." }}
        />
      </Card>
      <Card title="Members">
        <DataTable label="Members" columns={memberColumns(g.currency)} rows={g.members ?? []} getRowKey={(m) => m.assetBookId} empty={{ title: "No members" }} />
      </Card>
      <div>
        <Link className={buttonClassName("ghost")} to="/assets/group-assets">
          Back to group assets
        </Link>
      </div>
    </div>
  );
}
