// Typed client for services/expense (contract: services/expense/api/openapi.yaml).
// The browser calls /api/v1/...; the BFF rewrites the path and attaches the session.

export interface ExpenseLineInput {
  id: string;
  category: string;
  txnDate: string;
  amount: string;
  merchant: string;
  attendees: number;
  hasReceipt: boolean;
  justification: string;
  paidBy: "EMPLOYEE" | "COMPANY_CARD";
}

export type Outcome = "WARN" | "BLOCK" | "REQUIRE_JUSTIFICATION" | "REQUIRE_RECEIPT";

export interface PolicyResult {
  lineId: string;
  ruleId: string;
  ruleVersion: number;
  outcome: Outcome;
  code: "EXP-POL-001" | "EXP-POL-002" | "EXP-REC-001";
  message: string;
  satisfied: boolean;
}

export interface EvaluationResponse {
  submittable: boolean;
  results: PolicyResult[];
  totals: { total: string; reimbursable: string; companyPaid: string };
}

/** A RFC 9457 problem returned by the service, with its stable code. */
export class ExpenseProblem extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ExpenseProblem";
  }
}

export async function evaluateReport(
  submitDate: string,
  lines: ExpenseLineInput[],
  fetchImpl: typeof fetch = fetch,
): Promise<EvaluationResponse> {
  const res = await fetchImpl("/api/v1/expense/report-evaluations", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ submitDate, lines }),
  });
  const json = await res.json();
  if (!res.ok) {
    throw new ExpenseProblem(res.status, String(json.code ?? "COM-0500"), String(json.detail ?? "request failed"));
  }
  return json as EvaluationResponse;
}
