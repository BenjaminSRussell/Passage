import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { configureApi, get, signOut, type Me } from "./api";
import { balances, OpenItems, Payments, Statement, type OpenItem } from "./pages/Account";
import { InvoiceDetail, Invoices } from "./pages/Invoices";
import { OrderDetail, Orders } from "./pages/Orders";
import { ReturnDetail, Returns } from "./pages/Returns";
import { Page, State, useLoad } from "./ui";
import "./app.css";

function Home() {
  const items = useLoad(() => get<{ items: OpenItem[] }>("/api/v1/open-items?status=open&limit=200"), []);
  return (
    <Page title="Home">
      <State load={items}>
        {(d) => {
          const rows = balances(d.items);
          if (rows.length === 0) return <p>You have nothing to pay.</p>;
          return (
            <ul>
              {rows.map((b) => (
                <li key={b.currency}>
                  You owe {b.open.toFixed(2)} {b.currency}{b.overdue > 0 ? `, of which ${b.overdue.toFixed(2)} ${b.currency} is overdue` : ""}.
                </li>
              ))}
            </ul>
          );
        }}
      </State>
    </Page>
  );
}

const NAV: Array<[string, string]> = [["/", "Home"], ["/orders", "Orders"], ["/invoices", "Invoices"], ["/open-items", "Open items"], ["/payments", "Payments"], ["/statement", "Statement"], ["/returns", "Returns"]];

export function App() {
  const me = useLoad(() => get<Me>("/bff/me"), []);
  const [entity, setEntity] = useState("");
  const loc = useLocation();
  useEffect(() => {
    if (me.data) {
      const first = me.data.legalEntityIds[0] ?? "";
      setEntity((e) => e || first);
      configureApi({ csrfToken: me.data.csrfToken, legalEntityId: entity || first });
    }
  }, [me.data, entity]);
  useEffect(() => {
    document.getElementById("page-title")?.focus();
  }, [loc.pathname]);

  return (
    <>
      <a href="#main" className="skip">Skip to content</a>
      <header>
        <strong>Passage Customer Portal</strong>
        {me.data ? (
          <div className="who">
            {me.data.legalEntityIds.length > 1 ? (
              <label>Account with <select value={entity} onChange={(e) => setEntity(e.target.value)}>{me.data.legalEntityIds.map((id, n) => <option key={id} value={id}>Company {n + 1}</option>)}</select></label>
            ) : null}
            <span>{me.data.name || me.data.userId}</span>
            <button type="button" className="secondary" onClick={() => void signOut()}>Sign out</button>
          </div>
        ) : null}
      </header>
      <nav aria-label="Main">
        <ul>{NAV.map(([to, label]) => <li key={to}><NavLink to={to} end={to === "/"}>{label}</NavLink></li>)}</ul>
      </nav>
      <main id="main" key={entity}>
        <State load={me}>
          {() => (
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/orders" element={<Orders />} />
              <Route path="/orders/:id" element={<OrderDetail />} />
              <Route path="/invoices" element={<Invoices />} />
              <Route path="/invoices/:id" element={<InvoiceDetail />} />
              <Route path="/open-items" element={<OpenItems />} />
              <Route path="/payments" element={<Payments />} />
              <Route path="/statement" element={<Statement />} />
              <Route path="/returns" element={<Returns />} />
              <Route path="/returns/:id" element={<ReturnDetail />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          )}
        </State>
      </main>
    </>
  );
}
