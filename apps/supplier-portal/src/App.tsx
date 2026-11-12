import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { configureApi, get, signOut, type Me } from "./api";
import { Documents } from "./pages/Documents";
import { InvoiceLines } from "./pages/InvoiceLines";
import { Invoices } from "./pages/Invoices";
import { Payments } from "./pages/Payments";
import { PurchaseOrderDetail, PurchaseOrders, type PoSummary } from "./pages/PurchaseOrders";
import { RequestDetail, Requests, type VendorRequest } from "./pages/Requests";
import { NewShipment, ShipmentDetail, Shipments } from "./pages/Shipments";
import { Users } from "./pages/Users";
import { Page, State, useLoad } from "./ui";

function Home() {
  const pos = useLoad(() => get<{ items: PoSummary[] }>("/api/v1/purchase-orders?limit=100"), []);
  const reqs = useLoad(() => get<{ items: VendorRequest[] }>("/api/v1/requests?limit=100"), []);
  return (
    <Page title="Home">
      <State load={pos}>
        {(d) => {
          const waiting = d.items.filter((o) => (o.acknowledgement || "NONE") === "NONE");
          return <p>{waiting.length === 0 ? "No purchase orders are waiting for your answer." : `${waiting.length} purchase order${waiting.length === 1 ? " is" : "s are"} waiting for your answer.`}</p>;
        }}
      </State>
      <State load={reqs}>
        {(d) => {
          const todo = d.items.filter((r) => r.status === "INVITED").length;
          return todo > 0 ? <p>You have {todo} questionnaire{todo === 1 ? "" : "s"} to complete under Requests.</p> : null;
        }}
      </State>
    </Page>
  );
}

const NAV: Array<[string, string]> = [["/", "Home"], ["/purchase-orders", "Purchase orders"], ["/shipments", "Shipments"], ["/invoices", "Invoices"], ["/payments", "Payments"], ["/documents", "Documents"], ["/requests", "Requests"]];

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
        <strong>Passage Supplier Portal</strong>
        {me.data ? (
          <div className="who">
            {me.data.legalEntityIds.length > 1 ? (
              <label>Buyer entity <select value={entity} onChange={(e) => setEntity(e.target.value)}>{me.data.legalEntityIds.map((id) => <option key={id} value={id}>{id.slice(0, 8)}</option>)}</select></label>
            ) : null}
            <span>{me.data.name || me.data.userId}</span>
            <button type="button" className="secondary" onClick={() => void signOut()}>Sign out</button>
          </div>
        ) : null}
      </header>
      <nav aria-label="Main">
        <ul>{[...NAV, ...(me.data?.isAdmin ? [["/users", "Users"] as [string, string]] : [])].map(([to, label]) => <li key={to}><NavLink to={to} end={to === "/"}>{label}</NavLink></li>)}</ul>
      </nav>
      <main id="main" key={entity}>
        <State load={me}>
          {() => (
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/purchase-orders" element={<PurchaseOrders />} />
              <Route path="/purchase-orders/:id" element={<PurchaseOrderDetail />} />
              <Route path="/shipments" element={<Shipments />} />
              <Route path="/shipments/new" element={<NewShipment />} />
              <Route path="/shipments/:id" element={<ShipmentDetail />} />
              <Route path="/invoices" element={<Invoices />} />
              <Route path="/invoices/received" element={<InvoiceLines />} />
              <Route path="/payments" element={<Payments />} />
              <Route path="/users" element={<Users isAdmin={Boolean(me.data?.isAdmin)} />} />
              <Route path="/documents" element={<Documents />} />
              <Route path="/requests" element={<Requests />} />
              <Route path="/requests/:id" element={<RequestDetail />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          )}
        </State>
      </main>
    </>
  );
}
