const base = "/api/v1/product";

export async function searchProducts(q: string): Promise<{ items: { productNumber: string; name: string }[] }> {
  const res = await fetch(`${base}/products:search?q=${encodeURIComponent(q)}`);
  if (!res.ok) {
    throw new Error("search failed");
  }
  return res.json();
}

export async function validateGtin(value: string): Promise<{ valid: boolean }> {
  const res = await fetch(`${base}/gtins:validate`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ value }),
  });
  return res.json();
}
