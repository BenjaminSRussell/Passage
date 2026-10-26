import { vi } from "vitest";

export function stubLocalStorage(): Storage {
  const data = new Map<string, string>();
  const storage = {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => void data.set(key, String(value)),
    removeItem: (key: string) => void data.delete(key),
    clear: () => data.clear(),
  } as unknown as Storage;
  vi.stubGlobal("localStorage", storage);
  return storage;
}
