/** Stable message codes from PRD 9.47.18 that this slice can raise. */
export type AgentErrorCode =
  | "AGT-0201" // kill switch active
  | "AGT-0202" // token budget exhausted
  | "AGT-0203" // missing duty
  | "AGT-0204" // agent version has no passing evaluation
  | "AGT-0206" // provider unavailable or unusable response
  | "AGT-0208" // approval expired
  | "AGT-0209" // approver equals invoker above self-approval limit
  | "AGT-0210" // unredacted PII detected
  | "AGT-0212" // no knowledge source above the citation similarity threshold
  | "AGT-0214" // tool failed with 4xx, not retried
  | "AGT-0215"; // PHI-tagged field in text bound for a model (#606)

export class AgentError extends Error {
  readonly code: AgentErrorCode;

  constructor(code: AgentErrorCode, message: string) {
    super(`${code}: ${message}`);
    this.name = "AgentError";
    this.code = code;
  }
}
