export interface BenefitElection {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  planCode: string;
  employeeRef: string;
  coverageTier?: string;
  medicalLeaveReason?: string;
  accommodationNote?: string;
  payrollMemo?: string;
}

export type BenefitElectionInput = Omit<BenefitElection, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/hr/benefit-elections";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listBenefitElection = () => call<BenefitElection[]>("", "GET");
export const getBenefitElection = (id: string) => call<BenefitElection>(`/${id}`, "GET");
export const createBenefitElection = (body: BenefitElectionInput, idempotencyKey: string) => call<BenefitElection>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateBenefitElection = (id: string, body: Partial<BenefitElectionInput>, ifMatch: string) => call<BenefitElection>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeBenefitElection = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
