import { useEffect, useState } from "react";
import { apiFetch } from "../../shell/api/http";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

// The manufacturing modules share this small reader of the engineering resource master: ids (or codes) show as
// "Name (CODE)". Forms pick a resource through the shell's "resource" lookup entity (ResourceSelect).

export interface ResourceRef {
  id: string;
  code: string;
  name: string;
}

let cache: Promise<ResourceRef[]> | null = null;

function loadResources(): Promise<ResourceRef[]> {
  cache ??= apiFetch("/api/v1/engineering/resources?limit=200", { headers: { Accept: "application/json" } })
    .then((res) => (res.ok ? (res.json() as Promise<{ items?: ResourceRef[] }>) : { items: [] }))
    .then((body) => (body.items ?? []).filter((r) => r.id && r.name))
    .catch(() => {
      cache = null;
      return [] as ResourceRef[];
    });
  return cache;
}

/** Forgets the loaded resources (tests). */
export function resetResourceCache(): void {
  cache = null;
}

export function formatResource(r: ResourceRef): string {
  return r.code ? `${r.name} (${r.code})` : r.name;
}

/** The resources of the legal entity, once loaded; empty until then. */
export function useResources(): ResourceRef[] {
  const [rows, setRows] = useState<ResourceRef[]>([]);
  useEffect(() => {
    let live = true;
    void loadResources().then((found) => {
      if (live) setRows(found);
    });
    return () => {
      live = false;
    };
  }, []);
  return rows;
}

/** "Name (CODE)" for a resource id (or code); the fallback, else the id itself, until it resolves. */
export function ResourceName({ id, fallback }: { id: string; fallback?: string }) {
  const rows = useResources();
  if (id === "") return <span>{fallback ?? "None"}</span>;
  const found = rows.find((r) => r.id === id || r.code === id);
  return <span>{found ? formatResource(found) : (fallback ?? id)}</span>;
}

/** A resource picked by name through the shell's resource lookup. The value is the resource id. */
export function ResourceSelect({
  value,
  onChange,
  emptyLabel = "Choose a resource",
  ...aria
}: {
  value: string;
  onChange(value: string): void;
  emptyLabel?: string;
  id?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "true" | "false";
  "aria-required"?: boolean;
}) {
  return (
    <EntityLookup
      {...aria}
      entity="resource"
      prefill={false}
      placeholder={emptyLabel}
      invalid={aria["aria-invalid"] === true || aria["aria-invalid"] === "true"}
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}
