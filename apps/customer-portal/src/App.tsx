import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { configureApi, get, signOut, type Me } from "./api";
import { balances, OpenItems, Payments, Statement, type OpenItem } from "./pages/Account";
import { InvoiceDetail, Invoices } from "./pages/Invoices";
import { NewOrder } from "./pages/NewOrder";
import { NewReturn } from "./pages/NewReturn";
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

interface Company { legalEntityId: string; name: string | null }
interface Account { id: string; accountNumber: string; name: string; relation: "self" | "bill-to" | "ship-to" }

const RELATION: Record<Account["relation"], string> = { self: "your account", "bill-to": "billed to", "ship-to": "ships for you" };

export function App() {
  const me = useLoad(() => get<Me>("/bff/me"), []);
  const [entity, setEntity] = useState("");
  const [account, setAccount] = useState("");
  const loc = useLocation();
  const activeEntity = entity || me.data?.legalEntityIds[0] || "";
  // The API settings are set while rendering, before any page below loads its data: an effect would run after the children's.
  if (me.data) configureApi({ csrfToken: me.data.csrfToken, legalEntityId: activeEntity, accountId: account });
  // Customer-facing company names (identity) and the accounts the contact may act for (party), per company.
  const companies = useLoad(() => (me.data ? get<{ items: Company[] }>("/api/v1/companies") : Promise.resolve({ items: [] as Company[] })), [me.data]);
  const accounts = useLoad(() => (me.data && activeEntity ? get<{ items: Account[] }>("/api/v1/accounts").catch(() => ({ items: [] as Account[] })) : Promise.resolve({ items: [] as Account[] })), [me.data, activeEntity]);
  useEffect(() => {
    document.getElementById("page-title")?.focus();
  }, [loc.pathname]);
  const nameOf = (id: string, n: number): string => companies.data?.items.find((c) => c.legalEntityId === id)?.name || `Company ${n + 1}`;
  const accountList = accounts.data?.items ?? [];

  return (
    <>
      <a href="#main" className="skip">Skip to content</a>
      <header>
        <strong>Passage Customer Portal</strong>
        {me.data ? (
          <div className="who">
            {me.data.legalEntityIds.length > 1 ? (
              <label>Account with <select value={activeEntity} onChange={(e) => { setEntity(e.target.value); setAccount(""); }}>{me.data.legalEntityIds.map((id, n) => <option key={id} value={id}>{nameOf(id, n)}</option>)}</select></label>
            ) : null}
            {accountList.length > 1 ? (
              <label>Acting for <select value={account || accountList[0]?.id} onChange={(e) => setAccount(e.target.value === accountList[0]?.id ? "" : e.target.value)}>{accountList.map((a) => <option key={a.id} value={a.id}>{a.name} ({a.accountNumber}, {RELATION[a.relation]})</option>)}</select></label>
            ) : null}
            <span>{me.data.name || "Your account"}</span>
            <button type="button" className="secondary" onClick={() => void signOut()}>Sign out</button>
          </div>
        ) : null}
      </header>
      <nav aria-label="Main">
        <ul>{NAV.map(([to, label]) => <li key={to}><NavLink to={to} end={to === "/"}>{label}</NavLink></li>)}</ul>
      </nav>
      <main id="main" key={`${activeEntity}:${account}`}>
        <State load={me}>
          {() => (
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/orders" element={<Orders />} />
              <Route path="/orders/new" element={<NewOrder />} />
              <Route path="/orders/:id" element={<OrderDetail />} />
              <Route path="/invoices" element={<Invoices />} />
              <Route path="/invoices/:id" element={<InvoiceDetail />} />
              <Route path="/open-items" element={<OpenItems />} />
              <Route path="/payments" element={<Payments />} />
              <Route path="/statement" element={<Statement />} />
              <Route path="/returns" element={<Returns />} />
              <Route path="/returns/new" element={<NewReturn />} />
              <Route path="/returns/:id" element={<ReturnDetail />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          )}
        </State>
      </main>
    </>
  );
}
