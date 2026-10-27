import { useMemo, useRef } from "react";
import { LeanApiError, newKey } from "./api/client";

/**
 * Whether the service answered a command, so its outcome is known. A network
 * failure, an unreadable answer or a 5xx leaves it unknown: the command may have
 * run before the answer was lost.
 */
export function answered(error: unknown): boolean {
  return error instanceof LeanApiError && error.status >= 400 && error.status < 500;
}

export interface CommandKeys {
  /**
   * Sends a board command with one Idempotency-Key per command (what it does, and
   * the body it sends) until the service answers it. A retry after an unknown
   * outcome reuses the key, so the service replays its first answer instead of
   * refusing a stale version; once answered, the next attempt is a new request.
   */
  send<T>(command: string, call: (key: string) => Promise<T>): Promise<T>;
}

export function useCommandKeys(): CommandKeys {
  const keys = useRef(new Map<string, string>());
  return useMemo(
    () => ({
      async send<T>(command: string, call: (key: string) => Promise<T>): Promise<T> {
        let key = keys.current.get(command);
        if (key === undefined) {
          key = newKey();
          keys.current.set(command, key);
        }
        try {
          const out = await call(key);
          keys.current.delete(command);
          return out;
        } catch (error) {
          if (answered(error)) keys.current.delete(command);
          throw error;
        }
      },
    }),
    [],
  );
}
