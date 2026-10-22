import { Link, Outlet } from "react-router-dom";
import type { PassageModule } from "../modules/registry";

/**
 * Placeholder shell. Wave 5 replaces this with the real metadata-driven shell
 * (workspaces, global search, saved views, personalization). Modules must only
 * depend on the contract in modules/registry.ts, not on this file.
 */
export function Shell({ modules }: { modules: PassageModule[] }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "240px 1fr", minHeight: "100vh" }}>
      <nav aria-label="Workspaces" style={{ borderRight: "1px solid #ddd", padding: 16 }}>
        <h1 style={{ fontSize: 18 }}>Passage</h1>
        {modules.map((m) => (
          <section key={m.id}>
            <h2 style={{ fontSize: 13, textTransform: "uppercase", color: "#666" }}>{m.title}</h2>
            <ul style={{ listStyle: "none", padding: 0 }}>
              {m.nav.map((n) => (
                <li key={n.to}>
                  <Link to={n.to}>{n.label}</Link>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </nav>
      <main style={{ padding: 24 }}>
        <Outlet />
      </main>
    </div>
  );
}
