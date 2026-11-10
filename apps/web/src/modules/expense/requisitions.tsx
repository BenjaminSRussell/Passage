import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
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
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createRequisition,
  decideRequisition,
  describeProblem,
  getRequisition,
  listRequisitions,
  newIdempotencyKey,
  submitRequisition,
  type Decision,
  type Destination,
  type Estimate,
  type NewRequisitionInput,
  type RequisitionStatus,
  type TravelRequisition,
} from "./api/client";
import { CURRENCY, DAY, DECIMAL, splitProblem } from "./format";
import { amount, useActionKeys, usePaged, useRecord, UUID, type Failure } from "./persisted";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

/** Service limits on one requisition (services/expense createRequisition). */
const MAX_DESTINATIONS = 20;
const MAX_ESTIMATES = 100;

export const REQUISITION_STATUS: Record<RequisitionStatus, { label: string; tone: BadgeTone }> = {
  DRAFT: { label: "Draft", tone: "neutral" },
  SUBMITTED: { label: "Awaiting approval", tone: "info" },
  APPROVED: { label: "Approved", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
  CLOSED: { label: "Closed", tone: "neutral" },
};

function RequisitionBadge({ status }: { status: RequisitionStatus }) {
  const s = REQUISITION_STATUS[status] ?? { label: status, tone: "neutral" as BadgeTone };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

function places(r: TravelRequisition): string {
  return r.destinations.map((d) => `${d.city}, ${d.country}`).join("; ");
}

const columns: DataTableColumn<TravelRequisition>[] = [
  { key: "purpose", header: "Purpose" },
  { key: "status", header: "Status", render: (r) => <RequisitionBadge status={r.status} /> },
  { key: "destinations", header: "Destinations", render: places },
  { key: "tripFrom", header: "From", mono: true },
  { key: "tripTo", header: "To", mono: true },
  { key: "estimatedTotal", header: "Estimated", align: "end", mono: true, render: (r) => amount(r.currency, r.estimatedTotal) },
];

/** FR-43.16 pre-trip travel requisitions visible to the caller, filtered by status. */
export function RequisitionsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RequisitionStatus | "">("");
  const list = usePaged((cursor) => listRequisitions({ status: status || undefined, cursor }), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Travel requisitions"
        description="Ask for approval before a trip: where, when and the estimated cost by category. The expense report of the trip is compared with the approved estimate."
        actions={
          <Button variant="primary" icon="workflow" onClick={() => navigate("/expense/travel-requisitions/new")}>
            New requisition
          </Button>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="requisitions-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as RequisitionStatus | "")}>
                <option value="">All statuses</option>
                {(Object.keys(REQUISITION_STATUS) as RequisitionStatus[]).map((s) => (
                  <option key={s} value={s}>
                    {REQUISITION_STATUS[s].label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {list.failure ? (
        <Alert tone="danger" title="The travel requisitions could not be loaded" live="alert" action={<Button size="sm" onClick={list.retry}>Try again</Button>}>
          <ProblemMessage problem={list.failure} />
        </Alert>
      ) : null}
      <DataTable
        label="Travel requisitions"
        columns={columns}
        rows={list.items}
        loading={list.loading}
        getRowKey={(r) => r.id}
        empty={
          status
            ? { title: `No ${REQUISITION_STATUS[status].label.toLowerCase()} requisitions`, description: "Choose another status to see other requisitions." }
            : { title: "No travel requisitions yet", description: "Create a requisition to get a trip approved before you travel." }
        }
        onRowOpen={(r) => navigate(`/expense/travel-requisitions/${r.id}`)}
      />
      {list.next && !list.loading ? (
        <div className={styles.formActions}>
          <Button loading={list.loadingMore} onClick={list.more}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export interface NewRequisitionValues {
  purpose: string;
  currency: string;
  tripFrom: string;
  tripTo: string;
  legalEntityId: string;
  workerId: string;
  destinations: Destination[];
  estimates: Estimate[];
}

export const emptyRequisition = (): NewRequisitionValues => ({
  purpose: "",
  currency: "USD",
  tripFrom: "",
  tripTo: "",
  legalEntityId: "",
  workerId: "",
  destinations: [{ country: "", city: "" }],
  estimates: [{ category: "", amount: "" }],
});

export function validateRequisition(v: NewRequisitionValues): { errors: Errors; input?: NewRequisitionInput } {
  const errors: Errors = {};
  const purpose = v.purpose.trim();
  const currency = v.currency.trim().toUpperCase();
  if (purpose === "" || purpose.length > 500) errors.purpose = "Enter the purpose, up to 500 characters.";
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DAY.test(v.tripFrom)) errors.tripFrom = "Enter the first travel day as YYYY-MM-DD.";
  if (!DAY.test(v.tripTo)) errors.tripTo = "Enter the last travel day as YYYY-MM-DD.";
  else if (DAY.test(v.tripFrom) && v.tripTo < v.tripFrom) errors.tripTo = "The last travel day cannot be before the first.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the worker id (a UUID).";
  const destinations = v.destinations.map((d) => ({ country: d.country.trim(), city: d.city.trim() }));
  destinations.forEach((d, i) => {
    if (d.country === "" || d.country.length > 128) errors[`destinations[${i}].country`] = "Enter the country.";
    if (d.city === "" || d.city.length > 128) errors[`destinations[${i}].city`] = "Enter the city.";
  });
  const estimates = v.estimates.map((e) => ({ category: e.category.trim().toUpperCase(), amount: e.amount.trim() }));
  estimates.forEach((e, i) => {
    if (e.category === "" || e.category.length > 32) errors[`estimates[${i}].category`] = "Enter the category, such as LODGING.";
    if (!DECIMAL.test(e.amount)) errors[`estimates[${i}].amount`] = "Enter the estimated amount, such as 450.00.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { purpose, currency, tripFrom: v.tripFrom, tripTo: v.tripTo, legalEntityId: v.legalEntityId.trim(), workerId: v.workerId.trim(), destinations, estimates },
  };
}

const REQUISITION_FIELDS = ["purpose", "currency", "tripFrom", "tripTo", "legalEntityId", "workerId"];

/** Create a DRAFT travel requisition with its destinations and estimated cost lines. */
export function NewRequisitionPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<NewRequisitionValues>(emptyRequisition);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newIdempotencyKey());

  function update(patch: Partial<NewRequisitionValues>) {
    key.current = newIdempotencyKey();
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function setDestination(i: number, patch: Partial<Destination>) {
    update({ destinations: values.destinations.map((d, j) => (j === i ? { ...d, ...patch } : d)) });
  }

  function setEstimate(i: number, patch: Partial<Estimate>) {
    update({ estimates: values.estimates.map((e, j) => (j === i ? { ...e, ...patch } : e)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRequisition(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const rq = await createRequisition(input, key.current);
      navigate(`/expense/travel-requisitions/${rq.id}`);
    } catch (err) {
      const shown = [
        ...REQUISITION_FIELDS,
        ...values.destinations.flatMap((_, i) => [`destinations[${i}].city`, `destinations[${i}].country`]),
        ...values.estimates.flatMap((_, i) => [`estimates[${i}].category`, `estimates[${i}].amount`]),
      ];
      const split = splitProblem(describeProblem(err), shown);
      setErrors(split.fields);
      setFailure(split.failure);
      setBusy(false);
    }
  }

  const field = (name: "purpose" | "currency" | "tripFrom" | "tripTo" | "legalEntityId" | "workerId", label: string, hint?: string, mono = false) => (
    <FormField id={`requisition-${name}`} label={label} hint={hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          className={mono ? styles.mono : undefined}
          type={name === "tripFrom" || name === "tripTo" ? "date" : "text"}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New travel requisition" description="It is saved as a draft; submit it from the next page to send it for approval." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>{field("purpose", "Purpose")}</div>
          {field("tripFrom", "First travel day")}
          {field("tripTo", "Last travel day")}
          {field("currency", "Currency", "ISO code of the estimates, such as USD.", true)}
          {field("legalEntityId", "Legal entity", "The legal entity id; it must be in your scope.", true)}
          {field("workerId", "Worker", "The worker id who travels.", true)}
          {values.destinations.map((d, i) => (
            <fieldset key={`d${i}`} className={styles.lineSet}>
              <legend className={styles.legend}>{`Destination ${i + 1}`}</legend>
              <FormField id={`requisition-dest-${i}-city`} label="City" error={errors[`destinations[${i}].city`]}>
                {(c) => <Input {...c} value={d.city} onChange={(e) => setDestination(i, { city: e.target.value })} invalid={Boolean(errors[`destinations[${i}].city`])} />}
              </FormField>
              <FormField id={`requisition-dest-${i}-country`} label="Country" error={errors[`destinations[${i}].country`]}>
                {(c) => <Input {...c} value={d.country} onChange={(e) => setDestination(i, { country: e.target.value })} invalid={Boolean(errors[`destinations[${i}].country`])} />}
              </FormField>
              {values.destinations.length > 1 ? (
                <div className={styles.rowActions}>
                  <Button variant="ghost" onClick={() => update({ destinations: values.destinations.filter((_, j) => j !== i) })}>
                    {`Remove destination ${i + 1}`}
                  </Button>
                </div>
              ) : null}
            </fieldset>
          ))}
          {values.destinations.length < MAX_DESTINATIONS ? (
            <div className={styles.formActions}>
              <Button onClick={() => update({ destinations: [...values.destinations, { country: "", city: "" }] })}>Add destination</Button>
            </div>
          ) : null}
          {values.estimates.map((e, i) => (
            <fieldset key={`e${i}`} className={styles.lineSet}>
              <legend className={styles.legend}>{`Estimate ${i + 1}`}</legend>
              <FormField id={`requisition-est-${i}-category`} label="Category" hint="Such as LODGING, AIRFARE or MEALS." error={errors[`estimates[${i}].category`]}>
                {(c) => <Input {...c} className={styles.mono} value={e.category} onChange={(ev) => setEstimate(i, { category: ev.target.value })} invalid={Boolean(errors[`estimates[${i}].category`])} />}
              </FormField>
              <FormField id={`requisition-est-${i}-amount`} label="Estimated amount" error={errors[`estimates[${i}].amount`]}>
                {(c) => (
                  <Input {...c} className={styles.mono} inputMode="decimal" value={e.amount} onChange={(ev) => setEstimate(i, { amount: ev.target.value })} invalid={Boolean(errors[`estimates[${i}].amount`])} />
                )}
              </FormField>
              {values.estimates.length > 1 ? (
                <div className={styles.rowActions}>
                  <Button variant="ghost" onClick={() => update({ estimates: values.estimates.filter((_, j) => j !== i) })}>
                    {`Remove estimate ${i + 1}`}
                  </Button>
                </div>
              ) : null}
            </fieldset>
          ))}
          {values.estimates.length < MAX_ESTIMATES ? (
            <div className={styles.formActions}>
              <Button onClick={() => update({ estimates: [...values.estimates, { category: "", amount: "" }] })}>Add estimate</Button>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The requisition was not created" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create requisition
            </Button>
            <Button variant="ghost" onClick={() => navigate("/expense/travel-requisitions")}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

const estimateColumns: DataTableColumn<Estimate & { currency: string }>[] = [
  { key: "category", header: "Category", mono: true },
  { key: "amount", header: "Estimated", align: "end", mono: true, render: (e) => amount(e.currency, e.amount) },
];

const destinationColumns: DataTableColumn<Destination>[] = [
  { key: "city", header: "City" },
  { key: "country", header: "Country" },
];

/** One requisition: destinations, estimates, submit for approval, and the approver's decision. */
export function RequisitionDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { record: rq, setRecord, failure: loadFailure, reload } = useRecord(() => getRequisition(id), id);
  const keyFor = useActionKeys();
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<ReactNode>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function act(action: string, run: (version: number) => Promise<TravelRequisition>, done: string) {
    if (!rq) return;
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      setRecord(await run(rq.version));
      setNotice(done);
    } catch (err) {
      const p = describeProblem(err);
      setFailure(<ProblemMessage problem={p} />);
      if (p.code === "EXP-TR-005") reload();
    } finally {
      setBusy(null);
    }
  }

  function decide(decision: Decision) {
    if (!rq) return;
    void act(decision, (v) => decideRequisition(rq.id, decision, v, keyFor("decide", v, decision)), decision === "APPROVE" ? "Requisition approved." : "Requisition rejected.");
  }

  if (loadFailure) {
    if (loadFailure.code === "EXP-TR-003" || loadFailure.code === "COM-0001") {
      return (
        <EmptyState
          icon="workflow"
          title="Travel requisition not found"
          description="It does not exist or is outside your legal entities."
          action={<Button onClick={() => navigate("/expense/travel-requisitions")}>Back to travel requisitions</Button>}
        />
      );
    }
    return (
      <Alert tone="danger" title="The travel requisition could not be loaded" live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
        <ProblemMessage problem={loadFailure} />
      </Alert>
    );
  }

  if (!rq) {
    return (
      <div className={styles.stack} aria-busy="true" aria-label="Loading the travel requisition">
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={rq.purpose}
        description={`Trip from ${rq.tripFrom} to ${rq.tripTo} for worker ${rq.workerId}.`}
        meta={<RequisitionBadge status={rq.status} />}
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate("/expense/travel-requisitions")}>
              All travel requisitions
            </Button>
            {rq.status === "DRAFT" ? (
              <Button variant="primary" loading={busy === "submit"} onClick={() => void act("submit", (v) => submitRequisition(rq.id, v, keyFor("submit", v)), "Requisition submitted for approval.")}>
                Submit for approval
              </Button>
            ) : null}
            {rq.status === "SUBMITTED" ? (
              <>
                <Button variant="danger" loading={busy === "REJECT"} onClick={() => decide("REJECT")}>
                  Reject
                </Button>
                <Button variant="primary" loading={busy === "APPROVE"} onClick={() => decide("APPROVE")}>
                  Approve
                </Button>
              </>
            ) : null}
          </>
        }
      />
      <StatGroup label="Requisition summary">
        <StatTile label="Estimated total" value={amount(rq.currency, rq.estimatedTotal)} />
        <StatTile label="Destinations" value={rq.destinations.length} />
        <StatTile label="Estimate lines" value={rq.estimates.length} />
      </StatGroup>
      {notice ? <Alert tone="success" title={notice} live="status" /> : null}
      {failure ? (
        <Alert tone="danger" title="The requisition was not changed" live="alert">
          {failure}
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.facts}>
          <dt>Legal entity</dt>
          <dd className={styles.mono}>{rq.legalEntityId}</dd>
          <dt>Created</dt>
          <dd className={styles.mono}>{rq.createdAt.slice(0, 10)}</dd>
          <dt>Submitted</dt>
          <dd className={styles.mono}>{rq.submittedAt ? rq.submittedAt.slice(0, 10) : "Not yet"}</dd>
          <dt>Decided</dt>
          <dd className={styles.mono}>{rq.decidedAt ? rq.decidedAt.slice(0, 10) : "Not yet"}</dd>
        </dl>
      </Card>
      <DataTable label="Destinations" columns={destinationColumns} rows={rq.destinations} getRowKey={(d, i) => `${i}-${d.city}`} />
      <DataTable label="Estimates" columns={estimateColumns} rows={rq.estimates.map((e) => ({ ...e, currency: rq.currency }))} getRowKey={(e, i) => `${i}-${e.category}`} />
    </div>
  );
}
