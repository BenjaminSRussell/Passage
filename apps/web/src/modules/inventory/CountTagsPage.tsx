import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  enterTagCount,
  generateTags,
  listCountPlans,
  listTags,
  MAX_TAGS,
  TAG_STATUSES,
  transferTags,
  voidTag,
  type CountPlan,
  type CountTag,
  type Problem,
  type TagStatus,
} from "./api/client";
import { dash, DECIMAL, describeDimensions, sentence, shortId, UUID } from "./format";
import { ALL_DIMENSIONS, emptyDimensions, FORM_DIMENSIONS, ProblemAlert, RowAction, type FormDimension } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const tagTones: Record<TagStatus, BadgeTone> = { OPEN: "info", USED: "success", VOID: "neutral", MISSING: "warning" };

interface CountValues {
  productId: string;
  countedQty: string;
  dimensions: Record<FormDimension, string>;
}

type CountErrors = Partial<Record<"productId" | "countedQty" | FormDimension, string>>;

/** Client checks that mirror the service's tag entry rules (site mandatory, serial counts 0 or 1). */
export function validateTagCount(values: CountValues, planWarehouse = ""): CountErrors {
  const errors: CountErrors = {};
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.countedQty.trim();
  if (!DECIMAL.test(qty) || Number(qty) < 0) {
    errors.countedQty = "Enter the counted quantity: zero or more.";
  } else if (values.dimensions.serial.trim() !== "" && Number(qty) > 1) {
    errors.countedQty = "A serial number counts 0 or 1.";
  }
  if (values.dimensions.site.trim() === "") errors.site = "Site is required.";
  const warehouse = values.dimensions.warehouse.trim();
  if (planWarehouse && warehouse !== "" && warehouse !== planWarehouse) errors.warehouse = `This plan counts warehouse ${planWarehouse}.`;
  return errors;
}

/** Enter the count of one open tag. */
function TagCountForm({ tag, plan, onSaved, onCancel }: { tag: CountTag; plan: CountPlan; onSaved(tag: CountTag): void; onCancel(): void }) {
  const [values, setValues] = useState<CountValues>({ productId: "", countedQty: "", dimensions: { ...emptyDimensions(), warehouse: plan.warehouse ?? "" } });
  const [errors, setErrors] = useState<CountErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateTagCount(values, plan.warehouse);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      onSaved(await enterTagCount(tag.id, tag.version, { productId: values.productId.trim(), countedQty: values.countedQty.trim(), dimensions: values.dimensions }));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const setDim = (name: FormDimension) => (event: { target: { value: string } }) => setValues({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });

  return (
    <Card title={`Count tag ${tag.tagNumber}`}>
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="tag-product" label="Product" error={errors.productId}>
          {(control) => <Input {...control} value={values.productId} onChange={(e) => setValues({ ...values, productId: e.target.value })} invalid={Boolean(errors.productId)} />}
        </FormField>
        <FormField id="tag-qty" label="Counted quantity" hint="Zero is a valid count." error={errors.countedQty}>
          {(control) => (
            <Input {...control} inputMode="decimal" value={values.countedQty} onChange={(e) => setValues({ ...values, countedQty: e.target.value })} invalid={Boolean(errors.countedQty)} />
          )}
        </FormField>
        {FORM_DIMENSIONS.map((name) => (
          <FormField key={name} id={`tag-${name}`} label={sentence(name)} optional={name !== "site"} error={errors[name]}>
            {(control) => <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={Boolean(errors[name])} />}
          </FormField>
        ))}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The count was not saved" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save count
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Generate a range of tags for the plan. */
function GenerateTagsForm({ planId, onGenerated }: { planId: string; onGenerated(count: number): void }) {
  const [count, setCount] = useState("50");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const n = Number(count.trim());
    if (!/^\d+$/.test(count.trim()) || n < 1 || n > MAX_TAGS) {
      setError(`Enter a number of tags from 1 to ${MAX_TAGS}.`);
      return;
    }
    setError("");
    setFailure(null);
    setBusy(true);
    try {
      const tags = await generateTags(planId, n, key.current);
      key.current = crypto.randomUUID();
      onGenerated(tags.length);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Generate tags">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="tags-count" label="Number of tags" hint="Tags are numbered from the legal entity's TAG- sequence." error={error}>
          {(control) => (
            <Input
              {...control}
              inputMode="numeric"
              value={count}
              onChange={(e) => {
                setCount(e.target.value);
                key.current = crypto.randomUUID();
              }}
              invalid={Boolean(error)}
            />
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="No tags were generated" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Generate tags
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Transfer the used tags to a count sheet, optionally marking open tags missing. */
function TransferCard({ planId, tags }: { planId: string; tags: CountTag[] }) {
  const navigate = useNavigate();
  const [markMissing, setMarkMissing] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const pending = tags.filter((t) => !t.sheetId);
  const used = pending.filter((t) => t.status === "USED").length;
  const open = pending.filter((t) => t.status === "OPEN").length;
  const voided = pending.filter((t) => t.status === "VOID").length;

  async function transfer() {
    setFailure(null);
    setBusy(true);
    try {
      const sheet = await transferTags(planId, markMissing && open > 0);
      navigate(`/inventory/count-sheets/${sheet.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      setBusy(false);
    }
  }

  return (
    <Card title="Transfer to count sheet">
      <div className={styles.stack}>
        <p className={styles.muted}>
          {used} used, {open} open and {voided} void {used + open + voided === 1 ? "tag is" : "tags are"} waiting. Used tags are summed per product and dimensions into a new count
          sheet, which you then post like any count.
        </p>
        {open > 0 ? (
          <span className={styles.check}>
            <Input type="checkbox" id="tags-missing" checked={markMissing} onChange={(e) => setMarkMissing(e.target.checked)} />
            <label htmlFor="tags-missing">Mark the {open} open {open === 1 ? "tag" : "tags"} as missing (supervisor)</label>
          </span>
        ) : null}
        {failure ? (
          <Alert tone="danger" title={failure.code === "INV-TAG-006" ? "Supervisor approval required" : "The tags were not transferred"} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div>
          <Button variant="primary" loading={busy} disabled={used === 0 || (open > 0 && !markMissing)} onClick={transfer}>
            Transfer to count sheet
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** Tag counting for one count plan (FR-16.18): generate, count, void and transfer tags. */
export function CountTagsPage() {
  const { id = "" } = useParams();
  const state = useLoad(async (signal) => {
    const [tags, plans] = await Promise.all([listTags(id, signal), listCountPlans(signal)]);
    return { tags, plan: plans.find((p) => p.id === id) ?? null };
  }, id);
  const [status, setStatus] = useState<TagStatus | "">("");
  const [counting, setCounting] = useState<CountTag | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const reload = state.reload;

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/count-plans">
      Back to count plans
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-CNT-004") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Count plan not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the count tags" problem={state.error} onRetry={state.reload} />;
  }

  const tags = state.data?.tags ?? [];
  const plan = state.data?.plan ?? null;
  const shown = status ? tags.filter((t) => t.status === status) : tags;
  const columns: DataTableColumn<CountTag>[] = [
    { key: "tagNumber", header: "Tag", mono: true },
    { key: "status", header: "Status", render: (row) => <Badge tone={tagTones[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
    { key: "productId", header: "Product", mono: true, render: (row) => (row.productId ? shortId(row.productId) : dash) },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
    { key: "countedQty", header: "Counted", align: "end", mono: true, render: (row) => row.countedQty ?? dash },
    {
      key: "sheetId",
      header: "Count sheet",
      render: (row) => (row.sheetId ? <Link to={`/inventory/count-sheets/${row.sheetId}`}>{shortId(row.sheetId)}</Link> : dash),
    },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.sheetId || (row.status !== "OPEN" && row.status !== "USED") ? (
          dash
        ) : (
          <span className={styles.inline}>
            {row.status === "OPEN" && plan ? (
              <Button size="sm" aria-label={`Count ${row.tagNumber}`} onClick={() => setCounting(row)}>
                Count
              </Button>
            ) : null}
            <RowAction
              label="Void"
              name={row.tagNumber}
              run={() => voidTag(row.id, row.version)}
              onDone={() => {
                setFailure(null);
                setNotice(`${row.tagNumber} voided.`);
                reload();
              }}
              onError={setFailure}
            />
          </span>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Count tags"
        description={plan ? `${plan.name} · ${plan.warehouse ? `warehouse ${plan.warehouse}` : "all warehouses"}` : "Pre-numbered tags for a count plan."}
        actions={back}
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The tag was not changed" live="alert" action={failure.code === "conflict_version" ? <Button size="sm" onClick={reload}>Reload tags</Button> : undefined}>
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {counting && plan ? (
        <TagCountForm
          key={counting.id}
          tag={counting}
          plan={plan}
          onCancel={() => setCounting(null)}
          onSaved={(tag) => {
            setCounting(null);
            setFailure(null);
            setNotice(`${tag.tagNumber} counted: ${tag.countedQty ?? "0"}.`);
            reload();
          }}
        />
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="tags-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as TagStatus | "")}>
              <option value="">All</option>
              {TAG_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <DataTable
        label="Count tags"
        columns={columns}
        rows={shown}
        getRowKey={(row) => row.id}
        loading={state.loading}
        empty={{
          title: status ? "No tags in this status" : "No tags yet",
          description: "Generate a range of tags, print them and hang one on each counted spot.",
        }}
      />
      {state.data ? (
        <>
          <GenerateTagsForm
            planId={id}
            onGenerated={(n) => {
              setFailure(null);
              setNotice(`${n} ${n === 1 ? "tag" : "tags"} generated.`);
              reload();
            }}
          />
          <TransferCard key={`${tags.length}:${tags.filter((t) => t.sheetId).length}`} planId={id} tags={tags} />
        </>
      ) : null}
    </div>
  );
}
