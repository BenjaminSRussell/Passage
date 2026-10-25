import { useState, type FormEvent } from "react";
import { whereUsed, type UsedBy } from "./api/client";

export function EngineeringHome() {
  return (
    <section>
      <h2>Engineering data</h2>
      <p>Bills of material, routings and engineering change orders.</p>
    </section>
  );
}

function flatten(nodes: UsedBy[]): UsedBy[] {
  return nodes.flatMap((n) => [n, ...flatten(n.parents ?? [])]);
}

export function WhereUsedPage() {
  const [component, setComponent] = useState("");
  const [asOf, setAsOf] = useState("");
  const [rows, setRows] = useState<UsedBy[] | null>(null);
  const [error, setError] = useState("");

  async function search(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setRows(flatten(await whereUsed(component.trim(), asOf)));
    } catch {
      setRows(null);
      setError("Where-used lookup failed. Check the component and date.");
    }
  }

  return (
    <section>
      <h2>Where-used</h2>
      <form onSubmit={search}>
        <label>
          Component
          <input value={component} onChange={(e) => setComponent(e.target.value)} required />
        </label>
        <label>
          As of date
          <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
        </label>
        <button type="submit">Find parents</button>
      </form>
      <p role="alert">{error}</p>
      {rows && (
        <table aria-label="Where-used results">
          <thead>
            <tr>
              <th scope="col">Product</th>
              <th scope="col">Level</th>
              <th scope="col">BOM version</th>
              <th scope="col">Site</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={`${row.level}-${row.versionId}-${row.productId}`}>
                <td>{row.productId}</td>
                <td>{row.level}</td>
                <td>{row.versionId}</td>
                <td>{row.siteId ?? ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rows?.length === 0 && <p>No active BOM uses this component on that date.</p>}
    </section>
  );
}
