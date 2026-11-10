// The Cedar Ridge Pumps office staff the finance and administration screens name: who owns journals, approves,
// holds roles and files expenses. Usernames match the ones the treasury, budgeting and sales handlers already use
// (ana.ortiz, marcus.lee, dana.kim, jordan.avery, dana.whitfield). Ids are deterministic so the user list, role
// assignments and SoD conflicts point at the same people.
import { USER } from "../../company";
import { uid } from "../../store";

export const K_USER = 620;

export interface Person {
  id: string;
  username: string;
  name: string;
  title: string;
  /** Roles (authz duties) the person holds; the security screens and SoD conflicts read these. */
  roles: string[];
  status: "active" | "deprovisioned";
  /** Days ago the account was created. */
  since: number;
}

const person = (n: number, username: string, name: string, title: string, roles: string[], since: number, status: Person["status"] = "active"): Person => ({
  id: uid(K_USER, n),
  username,
  name,
  title,
  roles,
  status,
  since,
});

/** The signed-in demo user. */
export const ME = person(1, "jordan.avery", USER.name, "Controller", ["ledger.journal.write", "ledger.journal.approve", "ledger.journal.post", "reporting.statement.run", "assets.asset.depreciate", "identity.security-role.maintain"], 900);

export const PEOPLE: Person[] = [
  ME,
  person(2, "marcus.lee", "Marcus Lee", "Chief financial officer", ["ledger.journal.approve", "reporting.statement.view", "assets.capex-request.approve", "budget.plan.decide"], 1400),
  person(3, "ana.ortiz", "Ana Ortiz", "Senior accountant, payables and treasury", ["ledger.journal.write", "ledger.journal.post", "payment-proposal.approve", "receiving.vendor-invoice.post", "treasury.reconciliation.close"], 1100),
  person(4, "dana.kim", "Dana Kim", "Plant controller, Cedar Rapids", ["ledger.journal.write", "ledger.journal.approve", "assets.asset.maintain", "assets.asset.depreciate", "costing.version.write"], 820),
  person(5, "dana.whitfield", "Dana Whitfield", "Sales operations manager", ["sales.order.approve", "sales.credit.release", "receivables.collection.maintain"], 760),
  person(6, "kevin.brandt", "Kevin Brandt", "Purchasing manager", ["procurement.requisition.approve", "procurement.purchase-order.maintain", "party.vendor.maintain", "payment-proposal.approve"], 1020),
  person(7, "maya.chen", "Maya Chen", "Accounts receivable specialist", ["receivables.payment.post", "receivables.collection.maintain", "billing.invoice.post"], 410),
  person(8, "tom.becker", "Tom Becker", "IT systems administrator", ["identity.user.maintain", "identity.security-role.maintain", "identity.legal-entity.maintain"], 1300),
  person(9, "rita.alvarez", "Rita Alvarez", "HR and payroll lead", ["hr.worker.maintain", "payroll.run.post", "hr.compensation.admin"], 640),
  person(10, "omar.siddiqui", "Omar Siddiqui", "Warehouse supervisor, Dallas", ["inventory.journal.post", "warehouse.work.supervise"], 380),
  person(11, "brian.kessler", "Brian Kessler", "Former cost accountant", ["costing.version.write"], 1500, "deprovisioned"),
];

export const byUsername = (username: string): Person => {
  const found = PEOPLE.find((p) => p.username === username);
  if (!found) throw new Error(`demo person ${username} not in finance/people.ts`);
  return found;
};

export const emailOf = (p: Person): string => `${p.username}@cedarridgepumps.example`;
