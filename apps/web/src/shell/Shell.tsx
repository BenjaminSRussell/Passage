import { useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useNavigate } from "react-router-dom";
import type { PassageModule } from "../modules/registry";
import { FormRenderer } from "./forms";
import { demoNote, fakeCatalog } from "./metadata";
import { applyView, saveView, type Personalization, type SavedView } from "./personalization";
import { clickTrace, headersFor } from "./telemetry";

export interface ShellProps {
  modules: PassageModule[];
  catalog?: ReturnType<typeof fakeCatalog>;
}

/**
 * Keyboard-first shell. Modules depend on the registry contract, not this file.
 * Module federation remotes are declared in vite.config.ts; discovery stays import.meta.glob.
 */
export function Shell({ modules, catalog = fakeCatalog() }: ShellProps) {
  const navigate = useNavigate();
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [announce, setAnnounce] = useState("");
  const [personalization, setPersonalization] = useState<Personalization>({ views: [] });
  const searchRef = useRef<HTMLInputElement>(null);

  const entries = useMemo(
    () =>
      modules.flatMap((m) => m.nav.map((n) => ({ workspace: m.title, ...n }))).filter((e) => e.label.toLowerCase().includes(query.toLowerCase()) || e.workspace.toLowerCase().includes(query.toLowerCase())),
    [modules, query],
  );

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setSearchOpen(true);
        setAnnounce("Search opened");
      }
      if (e.key === "Escape") {
        setSearchOpen(false);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (searchOpen) {
      searchRef.current?.focus();
    }
  }, [searchOpen]);

  function openSearch() {
    const trace = clickTrace("search");
    setSearchOpen(true);
    setAnnounce(`Search opened ${headersFor(trace).traceparent}`);
  }

  function useView() {
    const view: SavedView = {
      id: "default",
      name: "Default",
      entity: demoNote.id,
      columns: demoNote.fields.map((f) => f.name),
      density: "comfortable",
    };
    setPersonalization((s) => applyView(saveView(s, view), view.id));
    setAnnounce("Saved view applied");
  }

  const entity = catalog.get(demoNote.id) ?? demoNote;

  return (
    <div data-density={personalization.views[0]?.density ?? "comfortable"}>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <div style={{ display: "grid", gridTemplateColumns: "240px 1fr", minHeight: "100vh" }}>
        <nav aria-label="Workspaces" style={{ borderRight: "1px solid #ddd", padding: 16 }}>
          <h1 style={{ fontSize: 18 }}>Passage</h1>
          <button type="button" onClick={openSearch} aria-keyshortcuts="Control+K">
            Search
          </button>
          <button type="button" onClick={useView}>
            Apply saved view
          </button>
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
        <div>
          <div role="status" aria-live="polite">
            {announce}
          </div>
          <main id="main" tabIndex={-1} style={{ padding: 24 }}>
            <Outlet />
            <FormRenderer entity={entity} />
          </main>
        </div>
      </div>
      {searchOpen ? (
        <div role="dialog" aria-modal="true" aria-labelledby="global-search-title">
          <h2 id="global-search-title">Search</h2>
          <label htmlFor="global-search">Find a page</label>
          <input
            id="global-search"
            ref={searchRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && entries[0]) {
                navigate(entries[0].to);
                setSearchOpen(false);
                setAnnounce(`Opened ${entries[0].label}`);
              }
            }}
          />
          <ul>
            {entries.map((e) => (
              <li key={e.to}>
                <Link
                  to={e.to}
                  onClick={() => {
                    setSearchOpen(false);
                    setAnnounce(`Opened ${e.label}`);
                  }}
                >
                  {e.workspace}: {e.label}
                </Link>
              </li>
            ))}
          </ul>
          <button type="button" onClick={() => setSearchOpen(false)}>
            Close search
          </button>
        </div>
      ) : null}
    </div>
  );
}
