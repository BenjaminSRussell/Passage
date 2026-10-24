import { useState } from "react";

export function AdminHome() {
  return (
    <section>
      <h2>Admin workspace</h2>
      <p>Number sequences, audit, alerts, import/export, documents and standard texts.</p>
    </section>
  );
}

export function AuditViewer({
  items = [],
}: {
  items?: { userId: string; occurredAt: string; oldValue: string; newValue: string; traceId: string; field: string }[];
}) {
  return (
    <section>
      <h2>Audit viewer</h2>
      <table aria-label="Audit entries">
        <thead>
          <tr>
            <th scope="col">Field</th>
            <th scope="col">User</th>
            <th scope="col">Time</th>
            <th scope="col">Old</th>
            <th scope="col">New</th>
            <th scope="col">Trace</th>
          </tr>
        </thead>
        <tbody>
          {items.map((row) => (
            <tr key={row.traceId + row.field}>
              <td>{row.field}</td>
              <td>{row.userId}</td>
              <td>{row.occurredAt}</td>
              <td>{row.oldValue}</td>
              <td>{row.newValue}</td>
              <td>{row.traceId}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export function SequencePage() {
  return (
    <section>
      <h2>Number sequences</h2>
      <p>Allocate gap-free numbers per legal entity from the numbering service.</p>
    </section>
  );
}

export function AlertInbox({ items = [] }: { items?: { message: string }[] }) {
  return (
    <section>
      <h2>Alert inbox</h2>
      <ul>
        {items.map((item) => (
          <li key={item.message}>{item.message}</li>
        ))}
      </ul>
    </section>
  );
}

export function ImportExportPage() {
  const [text, setText] = useState("title,body\nHello,World");
  return (
    <section>
      <h2>Import and export</h2>
      <label htmlFor="csv">CSV</label>
      <textarea id="csv" value={text} onChange={(e) => setText(e.target.value)} />
    </section>
  );
}

export function DocumentsPage() {
  return (
    <section>
      <h2>Documents</h2>
      <p>Check-out, check-in, versions and legal hold. Object storage is the S3-compatible port.</p>
    </section>
  );
}

export function TextsPage() {
  return (
    <section>
      <h2>Standard texts</h2>
      <p>Language versions with {"{placeholder}"} tokens, used by templates and notes.</p>
    </section>
  );
}
