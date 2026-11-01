import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, buttonClassName, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { getPersonnelAction, listPersonnelActions, withdrawPersonnelAction, type ActionStatus, type PersonnelAction } from "./api/personnel";
import { dash, dayOf, employmentTypeLabel, formatUtc } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Entry, Facts, humanize, ListPage, Loaded, labelled, Money, SelectField, Status, useAction } from "./parts";
import { useLoad } from "./useLoad";

const STATUSES: ActionStatus[] = ["PENDING", "APPLIED", "WITHDRAWN", "FAILED"];

const columns: DataTableColumn<PersonnelAction>[] = [
  {
    key: "actionType",
    header: "Action",
    render: (row) => <Link to={`/hr/actions/${row.id}`}>{humanize(row.actionType)}</Link>,
  },
  { key: "workerId", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.workerId.slice(0, 8)}</Link> },
  { key: "effectiveDate", header: "Effective", mono: true, render: (row) => dayOf(row.effectiveDate) },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
  { key: "submittedAt", header: "Submitted", mono: true, render: (row) => formatUtc(row.submittedAt) },
];

/** Personnel actions, newest first. An action effective today or earlier applies at once; a later one stays pending until its date. */
export function PersonnelActionsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("status") ?? "";
  const status = STATUSES.includes(raw as ActionStatus) ? raw : "";
  return (
    <ListPage<PersonnelAction>
      title="Personnel actions"
      description="Hires, transfers, terminations and pay changes. Pay amounts appear only for compensation administrators."
      label="Personnel actions"
      noun="actions"
      load={(cursor, signal) => listPersonnelActions({ status, cursor }, signal)}
      loadKey={`actions|${status}`}
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No personnel actions", description: status ? "Try another status." : "Submitted actions appear here." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/actions/new">
          Submit action
        </Link>
      }
      toolbar={
        <SelectField
          id="action-status"
          label="Status"
          value={status}
          onChange={(value) => setParams(value ? { status: value } : {}, { replace: true })}
          options={labelled(STATUSES)}
          placeholder="All"
        />
      }
    />
  );
}

export function PersonnelActionDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPersonnelAction(id, signal), id);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Personnel action" what="personnel action" backTo="/hr/actions">
      {(row) => {
        const pay = row.payload;
        return (
          <div className={styles.stack}>
            <PageHeader
              title={`${humanize(row.actionType)} action`}
              meta={<Status value={row.status} />}
              actions={<BackLink to="/hr/actions">Back to actions</BackLink>}
            />
            {action.feedback}
            {row.status === "FAILED" ? (
              <Alert tone="danger" title="This action failed when it came due">
                {row.failureDetail ?? "The action could not be applied."} ({row.failureCode ?? "unknown"})
              </Alert>
            ) : null}
            <Card title="Action">
              <Facts>
                <Entry term="Worker" mono>
                  <Link to={`/hr/workers/${row.workerId}`}>{row.workerId}</Link>
                </Entry>
                <Entry term="Employment" mono>
                  {row.employmentId ?? dash}
                </Entry>
                <Entry term="Effective" mono>
                  {dayOf(row.effectiveDate)}
                </Entry>
                <Entry term="Reason">{row.reason ?? dash}</Entry>
                <Entry term="Submitted" mono>
                  {formatUtc(row.submittedAt)}
                </Entry>
                {row.appliedAt ? (
                  <Entry term="Applied" mono>
                    {formatUtc(row.appliedAt)}
                  </Entry>
                ) : null}
                {row.withdrawnAt ? (
                  <Entry term="Withdrawn" mono>
                    {formatUtc(row.withdrawnAt)}
                  </Entry>
                ) : null}
                {row.sourceKind ? <Entry term="Came from">{`${humanize(row.sourceKind)} ${row.sourceId ?? ""}`}</Entry> : null}
              </Facts>
            </Card>
            <Card title="Details">
              <Facts>
                {pay.legalEntityId ? (
                  <Entry term="Legal entity" mono>
                    {pay.legalEntityId}
                  </Entry>
                ) : null}
                {pay.employmentType ? <Entry term="Employment type">{employmentTypeLabel[pay.employmentType] ?? pay.employmentType}</Entry> : null}
                {pay.assignmentKind ? <Entry term="Assignment">{humanize(pay.assignmentKind)}</Entry> : null}
                {pay.positionId ? (
                  <Entry term="Position">
                    <Link to={`/hr/positions/${pay.positionId}`}>{pay.positionId}</Link>
                  </Entry>
                ) : null}
                {pay.fteShare ? (
                  <Entry term="FTE share" mono>
                    {pay.fteShare}
                  </Entry>
                ) : null}
                {pay.terminationReason ? (
                  <Entry term="Termination reason" mono>
                    {pay.terminationReason}
                  </Entry>
                ) : null}
                {pay.amount !== undefined && pay.currency ? (
                  <Entry term="Pay">
                    <Money amount={pay.amount} currency={pay.currency} />
                  </Entry>
                ) : null}
                {pay.compPlanId ? (
                  <Entry term="Compensation plan" mono>
                    {pay.compPlanId}
                  </Entry>
                ) : null}
              </Facts>
              {row.actionType === "COMP_CHANGE" && pay.amount === undefined ? (
                <p className={styles.note}>The pay amount is not shown to your role.</p>
              ) : null}
            </Card>
            <Card title="Withdraw">
              {row.status === "PENDING" ? (
                <div className={styles.stack}>
                  <p className={styles.note}>A pending action can be withdrawn until its effective date arrives.</p>
                  <div>
                    <ConfirmAction
                      label="Withdraw action"
                      title="Withdraw this action?"
                      description={`The ${humanize(row.actionType).toLowerCase()} effective ${dayOf(row.effectiveDate)} will not be applied.`}
                      confirmLabel="Withdraw"
                      busy={action.busy === "withdraw"}
                      onConfirm={() => void action.run("withdraw", () => withdrawPersonnelAction(row.id), "The action was withdrawn.")}
                    />
                  </div>
                </div>
              ) : (
                <p className={styles.note}>Only pending actions can be withdrawn; applied, withdrawn and failed actions are final.</p>
              )}
            </Card>
          </div>
        );
      }}
    </Loaded>
  );
}
