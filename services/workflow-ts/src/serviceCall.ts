import { ApplicationFailure } from "@temporalio/activity";

export const REJECTED = "ServiceCallRejected";
const RETRYABLE_CLIENT_STATUSES = [408, 412, 429];

interface Problem {
  detail?: string;
  code?: string;
}

export async function callFailure(label: string, response: Response, retryableCodes: readonly string[] = []): Promise<Error> {
  const problem = await problemBody(response);
  const message = `${label} failed with ${response.status}${problem.code ? ` ${problem.code}` : ""}${problem.detail ? `: ${problem.detail}` : ""}`;
  const authFault = (response.status === 401 || response.status === 403) && (!problem.code || problem.code.startsWith("AUTH-"));
  const retryable =
    response.status < 400 ||
    response.status >= 500 ||
    authFault ||
    RETRYABLE_CLIENT_STATUSES.includes(response.status) ||
    (problem.code !== undefined && retryableCodes.includes(problem.code));
  return retryable ? new Error(message) : ApplicationFailure.nonRetryable(message, REJECTED);
}

async function problemBody(response: Response): Promise<Problem> {
  try {
    return (await response.json()) as Problem;
  } catch {
    return {};
  }
}
