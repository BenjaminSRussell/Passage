import { useRef, useState, type FormEvent } from "react";
import { describeProblem, type Problem } from "./api/client";

export type Errors = Record<string, string>;

/**
 * Form state for one write. The Idempotency-Key is reused only while the body is
 * unchanged, so a retry after a network failure replays instead of duplicating,
 * while an edited body gets a fresh key.
 */
export function useWriteForm<V extends object>(
  initial: V,
  validate: (values: V) => Errors,
  run: (values: V, key: string) => Promise<void>,
) {
  const [values, setValues] = useState<V>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const attempt = useRef<{ body: string; key: string } | null>(null);

  const set = <K extends keyof V>(field: K, value: V[K]) => setValues((prev) => ({ ...prev, [field]: value }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validate(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(values);
    if (attempt.current?.body !== body) attempt.current = { body, key: crypto.randomUUID() };
    setBusy(true);
    try {
      await run(values, attempt.current.key);
      attempt.current = null;
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      setErrors(problem.fields);
    } finally {
      setBusy(false);
    }
  }

  return {
    values,
    set,
    errors,
    busy,
    failure,
    submit,
    reset: () => setValues(initial),
  };
}
