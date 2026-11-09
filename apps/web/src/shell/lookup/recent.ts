import type { LookupEntityKey, LookupOption } from "./entities";

const MAX_RECENT = 5;

const keyFor = (entity: LookupEntityKey) => `passage.lookup.recent.${entity}`;

export function readRecent(entity: LookupEntityKey): LookupOption[] {
  try {
    const parsed: unknown = JSON.parse(
      window.localStorage.getItem(keyFor(entity)) ?? "[]",
    );
    return Array.isArray(parsed)
      ? parsed
          .filter(
            (entry): entry is LookupOption =>
              typeof entry?.id === "string" && typeof entry?.name === "string",
          )
          .slice(0, MAX_RECENT)
      : [];
  } catch {
    return [];
  }
}

export function pushRecent(entity: LookupEntityKey, entry: LookupOption): void {
  try {
    const next = [
      entry,
      ...readRecent(entity).filter((other) => other.id !== entry.id),
    ].slice(0, MAX_RECENT);
    window.localStorage.setItem(keyFor(entity), JSON.stringify(next));
  } catch {
    return;
  }
}
