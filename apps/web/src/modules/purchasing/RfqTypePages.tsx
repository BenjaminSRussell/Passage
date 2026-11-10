import { useEffect, useMemo, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  changeRFQType,
  createRFQType,
  describeProblem,
  getRFQType,
  listRFQTypes,
  newIdempotencyKey,
  type RankingRule,
  type RFQType,
  type RFQTypeInput,
  type RFQTypeStatus,
} from "./api/client";
import { Details, LoadError, StatusBadge, decimalPattern, rankingLabels, statusLabel, useResource, uuidPattern, type Failure, Named } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

/** One scoring criterion being edited; key keeps the React row stable. */
export interface CriterionDraft {
  key: string;
  name: string;
  weight: string;
}

export const newCriterion = (name = "", weight = "1"): CriterionDraft => ({ key: newIdempotencyKey(), name, weight });

/** Mirrors the service rules on a scoring group: at most 20, unique names, positive weights, one at least for weighted ranking. */
export function validateCriteria(rule: RankingRule, criteria: CriterionDraft[]): Errors {
  const errors: Errors = {};
  if (rule === "WEIGHTED_SCORE" && criteria.length === 0) errors.criteria = "Add at least one scoring criterion to rank by weighted score.";
  const names = new Set<string>();
  criteria.forEach((c, i) => {
    const name = c.name.trim();
    if (name === "") errors[`criteria[${i}].name`] = "Name the criterion.";
    else if (names.has(name.toLowerCase())) errors[`criteria[${i}].name`] = "Use each name once.";
    names.add(name.toLowerCase());
    if (!decimalPattern.test(c.weight.trim()) || /^0+(\.0+)?$/.test(c.weight.trim())) errors[`criteria[${i}].weight`] = "Enter a positive weight.";
  });
  return errors;
}

export const criteriaInput = (criteria: CriterionDraft[]) => criteria.map((c) => ({ name: c.name.trim(), weight: c.weight.trim() }));

/** The weighted criteria of a scoring group, shared by the RFQ type form, the draft edit and the amendment form. */
export function CriteriaEditor({
  id,
  criteria,
  onChange,
  errors,
  hint,
}: {
  id: string;
  criteria: CriterionDraft[];
  onChange(next: CriterionDraft[]): void;
  errors: Errors;
  hint?: string;
}) {
  const set = (index: number, patch: Partial<CriterionDraft>) => onChange(criteria.map((c, i) => (i === index ? { ...c, ...patch } : c)));
  return (
    <Card
      title="Scoring criteria"
      actions={
        <Button size="sm" disabled={criteria.length >= 20} onClick={() => onChange([...criteria, newCriterion()])}>
          Add criterion
        </Button>
      }
    >
      <div className={styles.form}>
        {criteria.length === 0 ? (
          <p className={styles.muted}>{errors.criteria ?? hint ?? "No scoring criteria. Add criteria such as quality or delivery to score each vendor from 1 to 10."}</p>
        ) : null}
        {criteria.map((c, i) => (
          <fieldset key={c.key} className={styles.lineCard} aria-label={`Criterion ${i + 1}`}>
            <div className={styles.lineHead}>
              <p className={styles.lineTitle}>Criterion {i + 1}</p>
              <Button size="sm" variant="ghost" onClick={() => onChange(criteria.filter((_, j) => j !== i))}>
                Remove criterion {i + 1}
              </Button>
            </div>
            <div className={styles.grid}>
              <FormField id={`${id}-criterion-${i}-name`} label="Criterion" error={errors[`criteria[${i}].name`]}>
                {(ctl) => <Input {...ctl} value={c.name} maxLength={100} onChange={(e) => set(i, { name: e.target.value })} />}
              </FormField>
              <FormField id={`${id}-criterion-${i}-weight`} label="Weight" error={errors[`criteria[${i}].weight`]}>
                {(ctl) => <Input {...ctl} inputMode="decimal" value={c.weight} onChange={(e) => set(i, { weight: e.target.value })} />}
              </FormField>
            </div>
          </fieldset>
        ))}
      </div>
    </Card>
  );
}

const STATUSES: RFQTypeStatus[] = ["ACTIVE", "INACTIVE"];

const columns: DataTableColumn<RFQType>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  {
    key: "sealed",
    header: "Bidding",
    render: (t) => (t.sealed ? <Badge tone="info">Sealed</Badge> : "Open"),
  },
  { key: "amendmentsAllowed", header: "Amendments", render: (t) => (t.amendmentsAllowed ? "Allowed" : "Not allowed") },
  { key: "rankingRule", header: "Ranking", render: (t) => rankingLabels[t.rankingRule] },
  { key: "criteria", header: "Criteria", align: "end", render: (t) => t.criteria.length },
  { key: "status", header: "Status", render: (t) => <StatusBadge status={t.status} /> },
];

/** RFQ types with a status filter and load-more paging (FR-17.19). */
export function RFQTypesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RFQTypeStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<RFQType[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listRFQTypes({ status }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="RFQ types"
        description="Kinds of request for quotation: whether bids are sealed, whether a sent RFQ may be amended, how replies are ranked and the scoring criteria a new RFQ starts with. An RFQ copies these when it is created, so changing a type never moves an RFQ already out."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/rfq-types/new">
            New RFQ type
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="rt-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as RFQTypeStatus | "");
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
      {page.error ? <LoadError what="RFQ types" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="RFQ types"
        columns={columns}
        rows={page.error ? [] : rows}
        getRowKey={(t) => t.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No RFQ types yet", description: "Create STANDARD and SEALED types so buyers start an RFQ with the right bidding rules." }}
        onRowOpen={(t) => navigate(`/purchasing/rfq-types/${t.id}`)}
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

interface TypeForm {
  legalEntityId: string;
  code: string;
  name: string;
  sealed: boolean;
  amendmentsAllowed: boolean;
  rankingRule: RankingRule;
  status: RFQTypeStatus;
  criteria: CriterionDraft[];
}

const blank = (): TypeForm => ({
  legalEntityId: "",
  code: "",
  name: "",
  sealed: false,
  amendmentsAllowed: true,
  rankingRule: "LOWEST_PRICE",
  status: "ACTIVE",
  criteria: [],
});

export function validateRFQType(f: TypeForm, creating: boolean): Errors {
  const e: Errors = validateCriteria(f.rankingRule, f.criteria);
  if (creating) {
    if (!uuidPattern.test(f.legalEntityId.trim())) e.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!/^[A-Za-z0-9_-]{1,20}$/.test(f.code.trim())) e.code = "Use 1 to 20 letters, digits, _ or -.";
  }
  if (f.name.trim() === "") e.name = "Enter the name.";
  return e;
}

function toInput(f: TypeForm): RFQTypeInput {
  return {
    legalEntityId: f.legalEntityId.trim(),
    code: f.code.trim().toUpperCase(),
    name: f.name.trim(),
    sealed: f.sealed,
    amendmentsAllowed: f.amendmentsAllowed,
    rankingRule: f.rankingRule,
    status: f.status,
    criteria: criteriaInput(f.criteria),
  };
}

const formOf = (t: RFQType): TypeForm => ({
  legalEntityId: t.legalEntityId,
  code: t.code,
  name: t.name,
  sealed: t.sealed,
  amendmentsAllowed: t.amendmentsAllowed,
  rankingRule: t.rankingRule,
  status: t.status,
  criteria: t.criteria.map((c) => newCriterion(c.name, c.weight)),
});

function TypeFields({ id, form, set, errors, creating }: { id: string; form: TypeForm; set(patch: Partial<TypeForm>): void; errors: Errors; creating: boolean }) {
  return (
    <>
      <Card title="RFQ type">
        <div className={styles.grid}>
          {creating ? (
            <>
              <FormField id={`${id}-legal-entity`} label="Legal entity" error={errors.legalEntityId}>
                {(c) => <EntityLookup entity="legalEntity" {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
              </FormField>
              <FormField id={`${id}-code`} label="Code" hint="Upper case, for example SEALED" error={errors.code}>
                {(c) => <Input {...c} value={form.code} maxLength={20} onChange={(e) => set({ code: e.target.value.toUpperCase() })} />}
              </FormField>
            </>
          ) : null}
          <FormField id={`${id}-name`} label="Name" error={errors.name}>
            {(c) => <Input {...c} value={form.name} onChange={(e) => set({ name: e.target.value })} />}
          </FormField>
          <FormField id={`${id}-ranking`} label="Rank replies by">
            {(c) => (
              <Select {...c} value={form.rankingRule} onChange={(e) => set({ rankingRule: e.target.value as RankingRule })}>
                {(Object.keys(rankingLabels) as RankingRule[]).map((k) => (
                  <option key={k} value={k}>
                    {rankingLabels[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {creating ? null : (
            <FormField id={`${id}-status`} label="Status" hint="An inactive type cannot start a new RFQ">
              {(c) => (
                <Select {...c} value={form.status} onChange={(e) => set({ status: e.target.value as RFQTypeStatus })}>
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
        <div className={styles.form}>
          <div className={styles.checkboxRow}>
            <Input id={`${id}-sealed`} type="checkbox" checked={form.sealed} onChange={(e) => set({ sealed: e.target.checked })} />
            <label htmlFor={`${id}-sealed`}>Sealed bids: replies stay hidden until the deadline and late replies are refused</label>
          </div>
          <div className={styles.checkboxRow}>
            <Input id={`${id}-amend`} type="checkbox" checked={form.amendmentsAllowed} onChange={(e) => set({ amendmentsAllowed: e.target.checked })} />
            <label htmlFor={`${id}-amend`}>Amendments allowed: a sent RFQ may get an addendum that vendors acknowledge</label>
          </div>
        </div>
      </Card>
      <CriteriaEditor id={id} criteria={form.criteria} onChange={(criteria) => set({ criteria })} errors={errors} hint="No default criteria. RFQs of this type start without a scoring group." />
    </>
  );
}

/** Create form for an RFQ type. */
export function RFQTypeCreatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<TypeForm>(blank);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form]);
  const set = (patch: Partial<TypeForm>) => setForm((f) => ({ ...f, ...patch }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateRFQType(form, true);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createRFQType(toInput(form), key);
      navigate(`/purchasing/rfq-types/${body.id}`);
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
      <PageHeader title="New RFQ type" description="A code is unique within its legal entity and cannot be renamed; deactivate it instead of deleting it." />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <TypeFields id="rt" form={form} set={set} errors={errors} creating />
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save RFQ type
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/rfq-types">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** One RFQ type: read it and change it under its version. */
export function RFQTypeDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getRFQType(id, signal)).body, [id]);
  const [saved, setSaved] = useState<RFQType | null>(null);
  const [draft, setDraft] = useState<TypeForm>(blank);
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

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!saved) return;
    const found = validateRFQType(draft, false);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await changeRFQType(saved.id, toInput(draft), saved.version);
      setSaved(body);
      setDraft(formOf(body));
      setNotice(`RFQ type saved as version ${body.version}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-RFQ-TYPE-003";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={saved ? `RFQ type ${saved.code}` : "RFQ type"}
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/rfq-types">
            All RFQ types
          </Link>
        }
      />
      {resource.error ? <LoadError what="the RFQ type" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !saved ? <Skeleton /> : null}
      {saved ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Code", value: saved.code, mono: true },
                { label: "Legal entity", value: <Named entity="legalEntity" id={saved.legalEntityId} /> },
                { label: "Bidding", value: saved.sealed ? "Sealed" : "Open" },
                { label: "Status", value: <StatusBadge status={saved.status} /> },
                { label: "Version", value: String(saved.version), mono: true },
              ]}
            />
          </Card>
          <TypeFields id="rtd" form={draft} set={(patch) => setDraft((d) => ({ ...d, ...patch }))} errors={errors} creating={false} />
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    setFailure(null);
                    resource.reload();
                  }}
                >
                  Reload RFQ type
                </Button>
              ) : undefined
            }
          >
            {failure
              ? stale
                ? <ProblemMessage problem={failure} message="Someone else changed this RFQ type since you opened it. Reload to see their change, then save again." />
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
