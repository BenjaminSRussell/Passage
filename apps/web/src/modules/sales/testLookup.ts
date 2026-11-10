import { setLookupFetcher } from "../../shell/lookup/entities";

/**
 * Makes the shared lookups resolve the given ids to names, so a page that shows a name for an id can be asserted by
 * name. Any other id does not resolve and shows the page's fallback. Test files only.
 */
export function nameLookups(names: Record<string, string>): void {
  setLookupFetcher(async (url) => {
    const id = decodeURIComponent(new URL(url, "http://localhost").pathname.split("/").pop() ?? "");
    const name = names[id];
    if (!name) return new Response(JSON.stringify({ items: [] }), { status: url.includes("?") ? 200 : 404 });
    return new Response(JSON.stringify({ id, partyId: id, name, ID: id, Name: name }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}
