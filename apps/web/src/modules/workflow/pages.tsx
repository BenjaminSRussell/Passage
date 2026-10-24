export function WorkflowHome({
  counts = { pending: 4, running: 1, failed: 0 },
}: {
  counts?: { pending: number; running: number; failed: number };
}) {
  return (
    <section>
      <h2>Workflow and batch</h2>
      <ul aria-label="Workflow tiles">
        <li>Pending work items {counts.pending}</li>
        <li>Running jobs {counts.running}</li>
        <li>Failed runs {counts.failed}</li>
      </ul>
    </section>
  );
}

export function InboxPage({
  items = [
    { document: "PR-1042", step: "Director", submittedBy: "alice", due: "in 3 hours", status: "pending" },
    { document: "PR-1043", step: "Controller", submittedBy: "bob", due: "overdue", status: "escalated" },
  ],
}: {
  items?: { document: string; step: string; submittedBy: string; due: string; status: string }[];
}) {
  return (
    <section>
      <h2>Work items</h2>
      <table aria-label="Work items">
        <thead>
          <tr>
            <th scope="col">Document</th>
            <th scope="col">Step</th>
            <th scope="col">Submitted by</th>
            <th scope="col">Due</th>
            <th scope="col">Status</th>
          </tr>
        </thead>
        <tbody>
          {items.map((row) => (
            <tr key={row.document}>
              <td>{row.document}</td>
              <td>{row.step}</td>
              <td>{row.submittedBy}</td>
              <td>{row.due}</td>
              <td>{row.status}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export function DesignerPage() {
  return (
    <section>
      <h2>Workflow designer</h2>
      <p>Amount-band approval chain for purchase requisitions: role, then user, then expression, with a 4-hour SLA.</p>
      <ol aria-label="Approval steps">
        <li>Manager role · PT4H · escalate to finance</li>
        <li>Director user · PT4H · delegate allowed</li>
        <li>Controller expression · PT4H · escalate to admin</li>
      </ol>
    </section>
  );
}

export function JobMonitorPage({
  runs = [
    { code: "rows-1m", status: "executing", rows: "400000 / 1000000", eta: "2 min" },
    { code: "outbox.sweep", status: "ended", rows: "128 / 128", eta: "—" },
  ],
}: {
  runs?: { code: string; status: string; rows: string; eta: string }[];
}) {
  return (
    <section>
      <h2>Job monitor</h2>
      <table aria-label="Batch runs">
        <thead>
          <tr>
            <th scope="col">Job</th>
            <th scope="col">Status</th>
            <th scope="col">Rows</th>
            <th scope="col">ETA</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((row) => (
            <tr key={row.code}>
              <td>{row.code}</td>
              <td>{row.status}</td>
              <td>{row.rows}</td>
              <td>{row.eta}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export function TemplatesPage() {
  return (
    <section>
      <h2>Message templates</h2>
      <ul aria-label="Shipped templates">
        <li>workflow.workitem.assigned</li>
        <li>workflow.workitem.escalated</li>
        <li>batch.run.failed</li>
      </ul>
    </section>
  );
}
