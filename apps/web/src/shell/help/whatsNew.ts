export interface ReleaseNotes {
  date: string;
  items: string[];
}

/** Parses docs/user/whats-new.md: each "## date" heading starts a release, each "- " line is one change. */
export function parseWhatsNew(source: string): ReleaseNotes[] {
  const releases: ReleaseNotes[] = [];
  for (const line of source.split("\n")) {
    const heading = /^## (\S.*)$/.exec(line);
    if (heading?.[1]) {
      releases.push({ date: heading[1].trim(), items: [] });
      continue;
    }
    const item = /^- (.+)$/.exec(line);
    if (item?.[1] && releases.length > 0) releases[releases.length - 1]?.items.push(item[1].trim());
  }
  return releases.filter((release) => release.items.length > 0);
}
