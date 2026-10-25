const base = "/api/v1/engineering";

export interface UsedBy {
  productId: string;
  versionId: string;
  siteId?: string;
  level: number;
  parents?: UsedBy[];
}

export async function whereUsed(component: string, asOf: string): Promise<UsedBy[]> {
  const query = new URLSearchParams({ component, ...(asOf ? { asOf } : {}) });
  const res = await fetch(`${base}/boms:where-used?${query}`);
  if (!res.ok) {
    throw new Error("where-used lookup failed");
  }
  return (await res.json()).items;
}
