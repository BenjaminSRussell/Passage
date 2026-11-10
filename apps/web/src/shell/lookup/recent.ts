import type { LookupEntityKey, LookupOption } from "./entities";

const MAX_RECENT = 5;

const keyFor = (identity: string, entity: LookupEntityKey) =>
  `passage.lookup.recent.${identity}.${entity}`;

export function readRecent(identity: string, entity: LookupEntityKey): LookupOption[] {
  if (identity === "") {
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(
      window.localStorage.getItem(keyFor(identity, entity)) ?? "[]",
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

export function pushRecent(identity: string, entity: LookupEntityKey, entry: LookupOption): void {
  if (identity === "") {
    return;
  }
  try {
    const next = [
      entry,
      ...readRecent(identity, entity).filter((other) => other.id !== entry.id),
    ].slice(0, MAX_RECENT);
    window.localStorage.setItem(keyFor(identity, entity), JSON.stringify(next));
  } catch {
    return;
  }
}
