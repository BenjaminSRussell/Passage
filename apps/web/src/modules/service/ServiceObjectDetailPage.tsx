import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getServiceObject, installServiceObject, listServiceObjects, type Problem, type ServiceObject, type Versioned } from "./api/client";
import { ActivityPanel } from "./ActivitiesPage";
import { Details } from "./Details";
import { dash, DATE, shortId, today } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { WarrantyState, warrantySummary } from "./ServiceObjectsPage";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const childColumns: DataTableColumn<ServiceObject>[] = [
  { key: "serial", header: "Serial", mono: true },
  { key: "itemId", header: "Item", mono: true, render: (row) => shortId(row.itemId) },
  { key: "warranty", header: "Warranty", render: (row) => <WarrantyState object={row} /> },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** One installed unit: warranty, structure, installation and activities (PRD 8.45.6 "Installed base explorer"). */
export function ServiceObjectDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getServiceObject(id, signal), id);
  const [latest, setLatest] = useState<Versioned<ServiceObject> | null>(null);
  const [installedOn, setInstalledOn] = useState(today());
  const [error, setError] = useState<string | undefined>(undefined);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service object" />
        <Alert tone="danger" title="Could not load the service object" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service object" />
        <span className="visually-hidden">Loading the service object…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: obj, etag } = current;

  async function install(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice("");
    if (!DATE.test(installedOn)) {
      setError("Choose the installation date.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const next = await installServiceObject(obj.id, etag, installedOn);
      setLatest(next);
      setNotice(next.value.warrantyStartsOn ? `Installed; warranty runs from ${next.value.warrantyStartsOn}.` : "Installed.");
    } catch (err) {
      const problem = describeProblem(err);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(false);
    }
  }

  const caseLink = `/service/cases/new?customerPartyId=${obj.customerPartyId}&legalEntityId=${obj.legalEntityId}&serviceObjectId=${obj.id}`;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Service object ${obj.serial}`}
        meta={<StatusPill status={obj.status} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to={caseLink}>
              Open case
            </Link>
            <Link className={buttonClassName("secondary")} to={`/service/objects/new?parentId=${obj.id}&customerPartyId=${obj.customerPartyId}&legalEntityId=${obj.legalEntityId}`}>
              Add component
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this unit; it was reloaded" : "Could not record the installation"} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Unit" headingLevel={3}>
        <Details
          label="Unit"
          items={[
            ["Item", obj.itemId, true],
            ["Customer", obj.customerPartyId, true],
            ["Legal entity", obj.legalEntityId, true],
            ["Part of", obj.parentId ? <Link to={`/service/objects/${obj.parentId}`}>{shortId(obj.parentId)}</Link> : dash],
            ["Shipment", obj.shipmentId ?? dash, true],
            ["Shipped", obj.shippedOn ?? dash, true],
            ["Installed", obj.installedOn ?? "Not yet", true],
          ]}
        />
      </Card>
      <Card title="Warranty" headingLevel={3}>
        <Details
          label="Warranty"
          items={[
            ["Today", <WarrantyState key="w" object={obj} />],
            ["Terms", warrantySummary(obj.warranty)],
            ["Starts", obj.warrantyStartsOn ?? (obj.warranty.startTrigger === "INSTALL" ? "At installation" : dash), true],
            ["Ends", obj.warrantyEndsOn ?? dash, true],
          ]}
        />
      </Card>
      {!obj.installedOn ? (
        <Card title="Record installation" headingLevel={3}>
          <form className={styles.form} onSubmit={install} noValidate aria-label="Record installation">
            <FormField id="install-date" label="Installed on" hint={obj.warranty.startTrigger === "INSTALL" ? "The warranty starts on this day." : undefined} error={error}>
              {(control) => <Input {...control} type="date" value={installedOn} onChange={(e) => setInstalledOn(e.target.value)} invalid={Boolean(error)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record installation
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Components" headingLevel={3}>
        <PagedTable
          label="Components"
          columns={childColumns}
          load={(cursor, signal) => listServiceObjects({ parentId: obj.id, cursor }, signal)}
          loadKey={obj.id}
          empty={{ title: "No components", description: "Units installed inside this one appear here." }}
          onRowOpen={(row) => navigate(`/service/objects/${row.id}`)}
        />
      </Card>
      <ActivityPanel entityType="service_object" entityId={obj.id} />
    </div>
  );
}
