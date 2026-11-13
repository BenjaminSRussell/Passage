import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  cancelPunchoutSession,
  cartTotal,
  createRequisitionFromCart,
  listPunchoutSessions,
  listPunchoutSuppliers,
  nextStep,
  openPunchoutSession,
  type CartLine,
  type PunchoutFormat,
  type PunchoutSession,
} from "./api/punchout";
import { LoadError, StatusBadge, formatInstant, useResource } from "./parts";
import styles from "./pages.module.css";

const formatLabels: Record<PunchoutFormat, string> = { CXML: "cXML punchout", OCI: "SAP OCI" };

/** Checks the open-catalog form: a supplier is chosen. */
export function validatePunchout(partnerId: string): Record<string, string> {
  return partnerId === "" ? { partnerId: "Choose the supplier whose catalog you want to shop." } : {};
}

/**
 * Punchout (#35, 35-3): the requester opens a supplier's catalog, shops on the supplier's site, and the cart comes back here.
 * A returned cart becomes a draft requisition with one line per cart line; a line priced above the catalog price plus the
 * supplier's tolerance is flagged, and its requisition always goes to an approver.
 */
export function PunchoutPage() {
  const [partnerId, setPartnerId] = useState("");
  const [format, setFormat] = useState<PunchoutFormat>("CXML");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [opened, setOpened] = useState<PunchoutSession | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const suppliers = useResource((signal) => listPunchoutSuppliers(signal), []);
  const sessions = useResource((signal) => listPunchoutSessions(signal), []);
  const supplierName = useMemo(() => {
    const names = new Map((suppliers.data ?? []).map((s) => [s.partnerId, s.name]));
    return (id: string) => names.get(id) ?? "Supplier no longer offered";
  }, [suppliers.data]);
  const shown = (sessions.data ?? []).find((s) => s.id === selected) ?? null;

  const run = async (key: string, action: () => Promise<unknown>) => {
    setBusy(key);
    setFailure(null);
    try {
      await action();
      sessions.reload();
    } catch (e) {
      setFailure(e);
    } finally {
      setBusy(null);
    }
  };

  const open = () => {
    const found = validatePunchout(partnerId);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void run("open", async () => setOpened(await openPunchoutSession(partnerId, format)));
  };

  const columns: DataTableColumn<PunchoutSession>[] = [
    { key: "supplier", header: "Supplier", render: (s) => supplierName(s.partnerId) },
    { key: "format", header: "Catalog", render: (s) => formatLabels[s.format] },
    { key: "status", header: "Status", render: (s) => <StatusBadge status={s.status} /> },
    { key: "createdAt", header: "Opened", render: (s) => formatInstant(s.createdAt) },
    { key: "lines", header: "Cart lines", align: "end", render: (s) => (s.cart ? String(s.cart.lines.length) : "—") },
    { key: "total", header: "Cart total", align: "end", render: (s) => cartTotal(s.cart) },
    {
      key: "next",
      header: "Next step",
      render: (s) => {
        switch (nextStep(s)) {
          case "shop":
            return (
              <span className={styles.actions}>
                {s.startUrl ? (
                  <a className={styles.link} href={s.startUrl} target="_blank" rel="noreferrer">
                    Shop the catalog
                  </a>
                ) : null}
                <Button size="sm" variant="secondary" loading={busy === `cancel:${s.id}`} onClick={() => void run(`cancel:${s.id}`, () => cancelPunchoutSession(s.id))}>
                  Cancel
                </Button>
              </span>
            );
          case "create":
            return (
              <span className={styles.actions}>
                <Button size="sm" variant="secondary" onClick={() => setSelected(s.id)}>
                  View cart
                </Button>
                <Button size="sm" loading={busy === `create:${s.id}`} onClick={() => void run(`create:${s.id}`, () => createRequisitionFromCart(s.id))}>
                  Create requisition
                </Button>
              </span>
            );
          case "open":
            return (
              <span className={styles.actions}>
                <Button size="sm" variant="secondary" onClick={() => setSelected(s.id)}>
                  View cart
                </Button>
                <Link className={styles.link} to={`/purchasing/requisitions/${s.requisitionId}`}>
                  Open requisition
                </Link>
              </span>
            );
          default:
            return s.status === "REJECTED" ? "The cart was not accepted" : "—";
        }
      },
    },
  ];

  const lineColumns: DataTableColumn<CartLine>[] = [
    { key: "line", header: "Line", align: "end" },
    { key: "supplierPartId", header: "Supplier part", mono: true },
    { key: "description", header: "Description" },
    { key: "quantity", header: "Quantity", align: "end", render: (l) => `${l.quantity} ${l.unitCode}` },
    { key: "unitPrice", header: "Unit price", align: "end", render: (l) => `${l.unitPrice} ${l.currency}` },
    { key: "leadTimeDays", header: "Lead time", align: "end", render: (l) => `${l.leadTimeDays} days` },
    { key: "approval", header: "Approval", render: (l) => (l.needsApproval ? <Badge tone="warning">Above catalog price</Badge> : "Catalog price") },
  ];

  const supplierOptions = suppliers.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Punchout catalogs"
        description="Shop a supplier's own catalog and bring the cart back as a draft requisition. Lines priced above the catalog price go to an approver."
      />
      {suppliers.error ? <LoadError what="punchout suppliers" error={suppliers.error} onRetry={suppliers.reload} /> : null}
      <Card title="Open a supplier catalog">
        <form
          className={styles.toolbar}
          onSubmit={(e) => {
            e.preventDefault();
            open();
          }}
        >
          <div className={styles.filter}>
            <FormField id="punchout-supplier" label="Supplier" error={errors.partnerId}>
              {(c) => (
                <Select {...c} value={partnerId} onChange={(e) => setPartnerId(e.target.value)}>
                  <option value="">Choose a supplier</option>
                  {supplierOptions.map((s) => (
                    <option key={s.partnerId} value={s.partnerId}>
                      {s.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <div className={styles.filter}>
            <FormField id="punchout-format" label="Catalog type">
              {(c) => (
                <Select {...c} value={format} onChange={(e) => setFormat(e.target.value as PunchoutFormat)}>
                  {(Object.keys(formatLabels) as PunchoutFormat[]).map((f) => (
                    <option key={f} value={f}>
                      {formatLabels[f]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <div className={styles.filter}>
            <Button type="submit" loading={busy === "open"}>
              Open catalog
            </Button>
          </div>
        </form>
        {opened?.startUrl ? (
          <Alert tone="success" live="status" title={`${supplierName(opened.partnerId)} catalog is ready`}>
            <a className={styles.link} href={opened.startUrl} target="_blank" rel="noreferrer">
              Shop the catalog in a new tab
            </a>
            . When you check out there, the cart comes back to this page.
          </Alert>
        ) : null}
      </Card>
      {failure ? (
        <Alert tone="danger" live="alert" title="That didn't work">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {sessions.error ? <LoadError what="your punchout sessions" error={sessions.error} onRetry={sessions.reload} /> : null}
      <div className={styles.actions}>
        <Button variant="secondary" loading={sessions.loading} onClick={sessions.reload}>
          Check for returned carts
        </Button>
      </div>
      <DataTable
        label="Your punchout sessions"
        columns={columns}
        rows={sessions.error ? [] : (sessions.data ?? [])}
        getRowKey={(s) => s.id}
        loading={sessions.loading && !sessions.data}
        empty={{ title: "No punchout sessions yet", description: "Open a supplier catalog above; its cart shows up here when you check out." }}
      />
      {shown?.cart ? (
        <Card title={`Cart from ${supplierName(shown.partnerId)}`}>
          {shown.cart.issues.length > 0 ? (
            <Alert tone="warning" live="status" title="Some cart lines were left out">
              <ul>
                {shown.cart.issues.map((i, n) => (
                  <li key={n}>
                    <ProblemMessage problem={{ code: i.code, message: i.detail }} />
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
          <DataTable
            label="Cart lines"
            columns={lineColumns}
            rows={shown.cart.lines}
            getRowKey={(l) => String(l.line)}
            empty={{ title: "The cart has no lines", description: "Nothing in the returned cart could be ordered." }}
          />
        </Card>
      ) : null}
    </div>
  );
}
