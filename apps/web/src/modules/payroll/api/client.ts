// Typed client for services/payroll (contract: services/payroll/api/openapi.yaml).
// The browser calls /api/v1/...; the BFF rewrites the path and attaches the session.

export interface CalculationAmount {
  code: string;
  amount: string;
}

export type FilingStatus = "SINGLE" | "MARRIED_FILING_JOINTLY" | "MARRIED_FILING_SEPARATELY" | "HEAD_OF_HOUSEHOLD";

/** Form W-4. Step 3 and 4(a)/(b) amounts are annual; extraWithholding is per period. */
export interface W4 {
  filingStatus: FilingStatus;
  legacy?: boolean;
  allowances?: number;
  multipleJobs?: boolean;
  dependentsCredit?: string;
  otherIncome?: string;
  deductions?: string;
  extraWithholding?: string;
}

export interface PreTaxDeduction extends CalculationAmount {
  reducesFica?: boolean;
}

export interface CalculationRequest {
  runType: "REGULAR" | "OFF_CYCLE_BONUS";
  country: string;
  taxYear: number;
  periodsPerYear: 12 | 24 | 26 | 52;
  w4: W4;
  state?: string;
  earnings: CalculationAmount[];
  preTax: PreTaxDeduction[];
  postTax: never[];
  ytd: { ficaWages: string; futaWages: string; supplementalWages: string };
}

export interface StatementLine {
  code: string;
  kind: "EARNING" | "PRETAX" | "TAX" | "POSTTAX" | "EMPLOYER";
  amount: string;
  basisAmount: string;
  ruleVersion: string;
  bracket?: { schedule: "STANDARD" | "CHECKBOX"; filingStatus: string; over: string; rate: string; baseTax: string; adjustedAnnualWage: string };
}

export interface ArrearsEntry {
  code: string;
  created: string;
  recovered: string;
  balance: string;
}

export interface Statement {
  workerId?: string;
  ruleSet: string;
  hash: string;
  gross: string;
  taxableWagesFed: string;
  ficaWages: string;
  net: string;
  employerCost: string;
  lines: StatementLine[];
  arrears: ArrearsEntry[];
}

/** A RFC 9457 problem returned by the service, with its stable code (e.g. PAY-RULE-001). */
export class PayrollProblem extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "PayrollProblem";
  }
}

export async function calculatePayroll(
  body: CalculationRequest,
  fetchImpl: typeof fetch = fetch,
): Promise<Statement> {
  const res = await fetchImpl("/api/v1/payroll/calculations", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const json = await res.json();
  if (!res.ok) {
    throw new PayrollProblem(res.status, String(json.code ?? "COM-0500"), String(json.detail ?? "request failed"));
  }
  return json as Statement;
}
