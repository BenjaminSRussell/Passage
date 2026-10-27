import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  convertRequest,
  createRequest,
  describeProblem,
  listAssets,
  listRequests,
  newKey,
  triageRequest,
  type MaintenanceRequest,
  type NewRequest,
  type Problem,
  type RequestSource,
  type TriageAction,
} from "./api/client";
import { dash, minuteOf, sentence } from "./format";
import { Failure, PagedTable, State, StatusFilter } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";

const STATUSES = ["NEW", "TRIAGED", "CONVERTED", "REJECTED", "DEFERRED"];

const triageDone: Record<TriageAction, string> = { TRIAGE: "triaged", DEFER: "deferred", REJECT: "rejected" };

/** Request triage (8.44.6, FR-44.6): convert to a work order in one action, triage, defer or reject. */
export function RequestsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "NEW";
  const state = usePaged((cursor, signal) => listRequests({ status: status === "ALL" ? "" : status, cursor }, signal), `requests|${status}`);
  const [converting, setConverting] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const keys = useRef(new Map<string, string>());

  async function triage(row: MaintenanceRequest, action: TriageAction) {
    setConverting(`${row.id}|${action}`);
    setFailure(null);
    setNotice(null);
    // One key per request and action, so a retry applies the action once.
    const slot = `${row.id}|${action}|${row.status}`;
    const key = keys.current.get(slot) ?? newKey();
    keys.current.set(slot, key);
    try {
      const out = await triageRequest(row.id, action, "", key);
      setNotice(`Request ${triageDone[action]}: it is now ${sentence(out.status).toLowerCase()}.`);
      state.reload();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setConverting(null);
    }
  }

  async function convert(row: MaintenanceRequest) {
    setConverting(row.id);
    setFailure(null);
    // One key per request, so a double click or a retry converts once.
    const key = keys.current.get(row.id) ?? newKey();
    keys.current.set(row.id, key);
    try {
      const wo = await convertRequest(row.id, "USD", key);
      navigate(`/eam/work-orders/${wo.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setConverting(null);
    }
  }

  const columns: DataTableColumn<MaintenanceRequest>[] = [
    { key: "createdAt", header: "Raised", mono: true, render: (row) => minuteOf(row.createdAt) },
    { key: "description", header: "Description" },
    { key: "source", header: "Source", render: (row) => (row.source === "IOT" ? "IoT rule" : sentence(row.source)) },
    { key: "severity", header: "Severity", align: "end" },
    { key: "readingValue", header: "Reading", align: "end", render: (row) => (row.readingValue === undefined ? dash : row.readingValue) },
    { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
    { key: "triageNote", header: "Triage note", render: (row) => row.triageNote ?? dash },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.workOrderId ? (
          <Link to={`/eam/work-orders/${row.workOrderId}`}>Open work order</Link>
        ) : row.status === "CONVERTED" || row.status === "REJECTED" ? (
          dash
        ) : (
          <div className={styles.actions}>
            {row.status !== "DEFERRED" ? (
              <Button size="sm" loading={converting === row.id} onClick={() => convert(row)}>
                Convert to work order
              </Button>
            ) : null}
            {row.status !== "TRIAGED" ? (
              <Button size="sm" variant="ghost" loading={converting === `${row.id}|TRIAGE`} onClick={() => triage(row, "TRIAGE")}>
                Triage
              </Button>
            ) : null}
            {row.status !== "DEFERRED" ? (
              <Button size="sm" variant="ghost" loading={converting === `${row.id}|DEFER`} onClick={() => triage(row, "DEFER")}>
                Defer
              </Button>
            ) : null}
            <Button size="sm" variant="ghost" loading={converting === `${row.id}|REJECT`} onClick={() => triage(row, "REJECT")}>
              Reject
            </Button>
          </div>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Maintenance requests"
        description="Severity 1 converts to an emergency order at priority 1; the rest become corrective orders at the asset's default priority."
        actions={
          <Link className={buttonClassName("primary")} to="/eam/requests/new">
            New request
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <StatusFilter
          id="request-status"
          value={status === "ALL" ? "" : status}
          options={STATUSES}
          onChange={(next) => setParams({ status: next || "ALL" })}
        />
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? <Failure title="Could not update the request" problem={failure} /> : null}
      <PagedTable
        label="Maintenance requests"
        noun="requests"
        columns={columns}
        state={state}
        empty={status === "NEW" ? { title: "No new requests", description: "New requests from people and condition rules appear here." } : { title: "No requests match", description: "Choose another status." }}
      />
    </div>
  );
}

interface RequestValues {
  assetId: string;
  source: RequestSource;
  severity: string;
  description: string;
}

/** The checks the service makes on a new request, and the request the form sends. */
export function validateRequest(values: RequestValues): { errors: Partial<Record<keyof RequestValues, string>>; input?: NewRequest } {
  const errors: Partial<Record<keyof RequestValues, string>> = {};
  if (!values.assetId) errors.assetId = "Choose the asset.";
  const description = values.description.trim();
  if (description.length < 1 || description.length > 1000) errors.description = "Describe the problem in up to 1,000 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { assetId: values.assetId, source: values.source, severity: Number(values.severity), description } };
}

/** Raise a maintenance request against an asset (FR-44.6). */
export function NewRequestPage() {
  const navigate = useNavigate();
  const assets = useLoad((signal) => listAssets({ limit: 200 }, signal), "request-assets");
  const [values, setValues] = useState<RequestValues>({ assetId: "", source: "USER", severity: "3", description: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof RequestValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RequestValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRequest(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createRequest(input, key.current);
      navigate("/eam/requests");
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New maintenance request" description="Tell the planners what is wrong. They convert the request to a work order." />
      <Card>
        {assets.error ? (
          <Failure title="Could not load the assets" problem={assets.error} retry={assets.reload} />
        ) : assets.loading || !assets.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : assets.data.items.length === 0 ? (
          <Alert tone="info" title="No assets yet">
            Requests are raised against an asset. <Link to="/eam/assets/new">Create an asset</Link> first.
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="request-asset" label="Asset" error={errors.assetId}>
              {(control) => (
                <Select {...control} value={values.assetId} invalid={Boolean(errors.assetId)} onChange={(e) => update({ assetId: e.target.value })}>
                  <option value="">Choose an asset</option>
                  {assets.data!.items.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.assetNumber} {a.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="request-source" label="Source">
              {(control) => (
                <Select {...control} value={values.source} onChange={(e) => update({ source: e.target.value as RequestSource })}>
                  <option value="USER">User</option>
                  <option value="MOBILE">Mobile</option>
                  <option value="QUALITY">Quality</option>
                </Select>
              )}
            </FormField>
            <FormField id="request-severity" label="Severity" hint="1 is an emergency.">
              {(control) => (
                <Select {...control} value={values.severity} onChange={(e) => update({ severity: e.target.value })}>
                  {[1, 2, 3, 4, 5].map((n) => (
                    <option key={n} value={String(n)}>
                      {n}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <FormField id="request-description" label="Description" error={errors.description}>
                {(control) => <Textarea {...control} value={values.description} maxLength={1000} invalid={Boolean(errors.description)} onChange={(e) => update({ description: e.target.value })} />}
              </FormField>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not create the request" live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create request
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/requests">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}
