import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { PlanningApiError } from "./api/client";

export const DEFAULT_PLAN_CODE = "MAIN";
export const PLAN_CODE = /^[A-Z0-9][A-Z0-9_-]{1,19}$/;

/** A load failure in the shape pages show: message plus stable problem code. */
export interface LoadError {
  message: string;
  code: string;
  status: number;
}

export function describeError(err: unknown): LoadError {
  if (err instanceof PlanningApiError) {
    return { message: err.message, code: err.code, status: err.status };
  }
  return { message: "The planning service could not be reached or sent an unexpected response.", code: "PLAN-UNKNOWN", status: 0 };
}

export interface LoadState<T> {
  data: T | null;
  loading: boolean;
  error: LoadError | null;
}

/**
 * Runs load whenever a dependency changes, aborting the previous request, and
 * keeps only the latest answer (the org module's list pattern).
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]): LoadState<T> {
  const [state, setState] = useState<LoadState<T>>({ data: null, loading: true, error: null });
  const latest = useRef(0);
  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState({ data: null, loading: true, error: null });
    load(controller.signal).then(
      (data) => {
        if (token === latest.current) setState({ data, loading: false, error: null });
      },
      (failure: unknown) => {
        if (token === latest.current && !controller.signal.aborted) {
          setState({ data: null, loading: false, error: describeError(failure) });
        }
      },
    );
    return () => controller.abort();
  }, deps);
  return state;
}

/** The plan in view, kept in the `?plan=` query so links and reloads keep it. */
export function usePlanCode(): [string, (code: string) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get("plan") ?? DEFAULT_PLAN_CODE;
  const plan = PLAN_CODE.test(raw) ? raw : DEFAULT_PLAN_CODE;
  const setPlan = (code: string) => {
    const next = new URLSearchParams(params);
    next.set("plan", code);
    setParams(next);
  };
  return [plan, setPlan];
}

/** Path with the current plan carried along. */
export function withPlan(path: string, plan: string): string {
  return `${path}?plan=${encodeURIComponent(plan)}`;
}
