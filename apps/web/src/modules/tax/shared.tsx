import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, Button } from "../../shell/ui";
import { ProblemMessage } from "../../shell/errors";
import { TaxApiError } from "./api/client";

export const taxKey = (...parts: string[]) => ["tax", ...parts];

let regionNames: Intl.DisplayNames | undefined;

/** "Germany (DE)": a country by name with its code, so nobody has to know the codes by heart. */
export function countryLabel(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return code;
  try {
    regionNames ??= new Intl.DisplayNames(["en"], { type: "region" });
    const name = regionNames.of(code);
    return name && name !== code ? `${name} (${code})` : code;
  } catch {
    return code;
  }
}

export function firstOfMonth(offsetMonths = 0): string {
  const d = new Date();
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + offsetMonths, 1)).toISOString().slice(0, 10);
}

export function lastOfMonth(offsetMonths = 0): string {
  const d = new Date();
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + offsetMonths + 1, 0)).toISOString().slice(0, 10);
}

export function monthLabel(isoDate: string): string {
  const [y, m] = isoDate.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, 1)).toLocaleDateString("en-US", { month: "long", year: "numeric", timeZone: "UTC" });
}

/** Field errors the service returned, for showing under the form fields. */
export function fieldErrors(error: unknown): Record<string, string> {
  return error instanceof TaxApiError ? error.fields : {};
}

export interface Action<V> {
  run(value: V): void;
  pending: boolean;
  failure: unknown;
  clear(): void;
}

/** A write that refreshes the named queries when it succeeds and keeps its failure for the form to show. */
export function useAction<V, R = unknown>(fn: (value: V) => Promise<R>, refresh: string[][], onDone?: (result: R) => void): Action<V> {
  const client = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const mutation = useMutation({
    mutationFn: fn,
    onSuccess: (result) => {
      setFailure(null);
      for (const queryKey of refresh) void client.invalidateQueries({ queryKey });
      onDone?.(result);
    },
    onError: setFailure,
  });
  return { run: (v) => mutation.mutate(v), pending: mutation.isPending, failure, clear: () => setFailure(null) };
}

export function FailureAlert({ title, failure }: { title: string; failure: unknown }) {
  if (!failure) return null;
  return (
    <Alert tone="danger" title={title} live="alert">
      <ProblemMessage problem={failure} />
    </Alert>
  );
}

export function LoadAlert({ title, error, onRetry }: { title: string; error: unknown; onRetry(): void }) {
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        <Button size="sm" onClick={onRetry}>
          Try again
        </Button>
      }
    >
      <ProblemMessage problem={error} />
    </Alert>
  );
}
