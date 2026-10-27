import { useState } from "react";
import { describeProblem, type Problem } from "./api/client";

/** A form check: field errors keyed by request path, and the request when there are none. */
export interface Checked<Req> {
  errors: Record<string, string>;
  request?: Req;
}

/**
 * State of one preview form: field errors, the failed request, the loading flag
 * and the last result. `run` sends a checked request and keeps the service's
 * field errors next to the inputs they belong to.
 */
export function usePreview<Req, Res>(call: (req: Req) => Promise<Res>) {
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Res | null>(null);

  async function run(checked: Checked<Req>) {
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.request) return;
    setBusy(true);
    try {
      setResult(await call(checked.request));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return { errors, failure, busy, result, run };
}
