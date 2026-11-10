import { useState } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { CONTRACT_STATUSES, listContracts, METHODS, type ContractStatus, type RecognitionMethod, type RevenueContractSummary } from "./api/client";
import { CONTRACT_STATUS_LABELS, METHOD_LABELS, UUID } from "./format";
import { MoreRows } from "./MoreRows";
import styles from "./pages.module.css";
import { usePaged } from "./usePaged";
import { ProblemMessage } from "../../shell/errors";

const LIMIT = 100;

const columns: DataTableColumn<RevenueContractSummary>[] = [
  { key: "contractDate", header: "Contract date", mono: true },
  { key: "status", header: "Status", render: (c) => <StatusPill status={CONTRACT_STATUS_LABELS[c.status] ?? c.status} /> },
  { key: "customerId", header: "Customer", mono: true },
  { key: "orderId", header: "Order", mono: true },
  { key: "transactionPrice", header: "Price", mono: true, align: "end", render: (c) => `${c.transactionPrice} ${c.currency}` },
  { key: "recognized", header: "Recognized", mono: true, align: "end", render: (c) => `${c.recognized} ${c.currency}` },
  { key: "remaining", header: "Remaining", mono: true, align: "end", render: (c) => `${c.remaining} ${c.currency}` },
];

/**
 * Revenue contracts inquiry (§5.29.14): price, recognized and remaining per contract, newest first,
 * filtered by status, customer and obligation method and paged by the service cursor.
 */
export function RevenueContractsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ContractStatus | "">("");
  const [customer, setCustomer] = useState("");
  const [method, setMethod] = useState<RecognitionMethod | "">("");
  const customerId = UUID.test(customer.trim()) ? customer.trim() : "";
  const customerInvalid = customer.trim() !== "" && customerId === "";
  const state = usePaged((cursor, signal) => listContracts({ status, customerId, method, limit: LIMIT, cursor }, signal), `${status}|${customerId}|${method}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Revenue contracts"
        description="Contracts with their standalone selling price allocation and how much revenue is recognized. Open one to see its schedules and satisfy milestones."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/billing/deferred-revenue">
              Deferred revenue
            </Link>
            <Link className={buttonClassName("primary")} to="/billing/revenue-contracts/new">
              New revenue contract
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rc-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as ContractStatus | "")}>
              <option value="">Any status</option>
              {CONTRACT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {CONTRACT_STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="rc-method" label="Obligation method">
          {(c) => (
            <Select {...c} value={method} onChange={(e) => setMethod(e.target.value as RecognitionMethod | "")}>
              <option value="">Any method</option>
              {METHODS.map((m) => (
                <option key={m} value={m}>
                  {METHOD_LABELS[m]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="rc-customer" label="Customer" optional error={customerInvalid ? "Enter a full customer id (a UUID) to filter." : undefined}>
          {(c) => <EntityLookup entity="customer" {...c} value={customer} onChange={(e) => setCustomer(e.target.value)} invalid={customerInvalid} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the revenue contracts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Revenue contracts"
          columns={columns}
          rows={state.rows}
          getRowKey={(c) => c.id}
          loading={state.loading}
          empty={{
            title: status || customerId || method ? "No contracts match these filters" : "No revenue contracts yet",
            description: "Create a revenue contract to allocate a bundle's price across its obligations and plan when the revenue is recognized.",
          }}
          onRowOpen={(c) => navigate(`/billing/revenue-contracts/${c.id}`)}
        />
      )}
      <MoreRows state={state} noun="contracts" />
    </div>
  );
}
