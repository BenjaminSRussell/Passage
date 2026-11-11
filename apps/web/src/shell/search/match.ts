/**
 * The matching rules of the record search service (services/search/internal/store), for rows a page filters in the
 * browser: the same query finds the same records in the palette and in a list. The cases in
 * services/search/internal/store/testdata/matching.json run against both, so a change on one side fails the other.
 *
 * - A query under three characters, or shaped like a number (a digit, no space, no run of more than three letters),
 *   matches the start of the number.
 * - Any other query matches when the number and name hold it, or when it is similar enough to a stretch of them
 *   (trigram word similarity of 0.4 or more, as pg_trgm's word_similarity counts it), which forgives a typo.
 * - Best first: the exact number, then a number that starts with the query, then the most similar, then by number.
 *
 * Letters and digits of any script count as part of a word here; in Postgres that follows the database locale.
 */

/** What a row is searched by. `name` is the title next to the number; a document that has none leaves it out. */
export interface SearchText {
  number: string;
  name?: string;
}

const MIN_FUZZY_CHARS = 3;
const MAX_NUMBER_LETTERS = 3;
export const WORD_SIMILARITY_THRESHOLD = 0.4;

export const normalizeQuery = (query: string): string => query.trim().toLowerCase();

function hasWord(text: string): boolean {
  let run = 0;
  for (const ch of text) {
    if (/\p{L}/u.test(ch)) {
      run += 1;
      if (run > MAX_NUMBER_LETTERS) {
        return true;
      }
    } else {
      run = 0;
    }
  }
  return false;
}

/** Whether the (normalized) query is matched against the start of the number only. */
export function isNumberQuery(text: string): boolean {
  const numberShaped = /\p{Nd}/u.test(text) && !/\s/u.test(text) && !hasWord(text);
  return [...text].length < MIN_FUZZY_CHARS || numberShaped;
}

/** pg_trgm's trigrams of a text, in order: each run of letters and digits, lower-cased, padded with two spaces before and one after. */
function trigrams(text: string): string[] {
  const out: string[] = [];
  for (const word of text.toLowerCase().match(/[\p{L}\p{N}]+/gu) ?? []) {
    const padded = [" ", " ", ...word, " "];
    for (let i = 0; i + 3 <= padded.length; i++) {
      out.push(padded.slice(i, i + 3).join(""));
    }
  }
  return out;
}

const similarity = (count: number, queryCount: number, extentCount: number): number => Math.fround(count / (queryCount + extentCount - count));

/**
 * pg_trgm's word_similarity(query, text): the greatest similarity between the query's trigrams and any continuous
 * stretch of the text's trigrams. 0 is nothing in common, 1 is the query found whole as a word.
 */
export function wordSimilarity(query: string, text: string): number {
  const wanted = new Set(trigrams(query));
  const ordered = trigrams(text);
  if (wanted.size === 0 || ordered.length === 0) {
    return 0;
  }
  const found = ordered.map((t) => wanted.has(t));
  const lastPos = new Map<string, number>();
  let best = 0;
  let lower = -1;
  let distinct = 0;
  let count = 0;
  for (let upper = 0; upper < ordered.length; upper++) {
    const trigram = ordered[upper];
    if (lower >= 0 || found[upper]) {
      if (!lastPos.has(trigram)) {
        distinct += 1;
        if (found[upper]) {
          count += 1;
        }
      }
      lastPos.set(trigram, upper);
    }
    if (!found[upper]) {
      continue;
    }
    if (lower === -1) {
      lower = upper;
      distinct = 1;
    }
    let current = similarity(count, wanted.size, distinct);
    // Move the lower bound up while a shorter stretch is more similar.
    const previousLower = lower;
    let tryCount = count;
    let tryDistinct = distinct;
    for (let tryLower = lower; tryLower <= upper; tryLower++) {
      if (found[tryLower]) {
        const candidate = similarity(tryCount, wanted.size, tryDistinct);
        if (candidate > current) {
          current = candidate;
          distinct = tryDistinct;
          lower = tryLower;
          count = tryCount;
        }
      }
      if (lastPos.get(ordered[tryLower]) === tryLower) {
        tryDistinct -= 1;
        if (found[tryLower]) {
          tryCount -= 1;
        }
      }
    }
    best = Math.max(best, current);
    for (let dropped = previousLower; dropped < lower; dropped++) {
      if (lastPos.get(ordered[dropped]) === dropped) {
        lastPos.delete(ordered[dropped]);
      }
    }
  }
  return best;
}

const numberKey = (target: SearchText): string => target.number.toLowerCase();
const searchText = (target: SearchText): string => `${target.number} ${target.name ?? ""}`.toLowerCase();

/** Whether the service would return the row for the query. An empty query matches every row. */
export function matchesSearch(query: string, target: SearchText): boolean {
  const text = normalizeQuery(query);
  if (text === "") {
    return true;
  }
  if (isNumberQuery(text)) {
    return numberKey(target).startsWith(text);
  }
  const haystack = searchText(target);
  return haystack.includes(text) || wordSimilarity(text, haystack) >= WORD_SIMILARITY_THRESHOLD;
}

/** The matching rows in the order the list already has. Use it for a text filter over rows loaded in the browser. */
export function filterBySearch<T>(rows: T[], query: string, read: (row: T) => SearchText): T[] {
  return normalizeQuery(query) === "" ? rows : rows.filter((row) => matchesSearch(query, read(row)));
}

/** The matching rows, best first, as the service ranks them. */
export function rankBySearch<T>(rows: T[], query: string, read: (row: T) => SearchText): T[] {
  const text = normalizeQuery(query);
  if (text === "") {
    return rows;
  }
  const scored = rows.flatMap((row) => {
    const target = read(row);
    if (!matchesSearch(text, target)) {
      return [];
    }
    const key = numberKey(target);
    return [{ row, key, exact: key === text, prefix: key.startsWith(text), similarity: wordSimilarity(text, searchText(target)) }];
  });
  const first = (a: boolean, b: boolean) => Number(b) - Number(a);
  return scored
    .sort((a, b) => first(a.exact, b.exact) || first(a.prefix, b.prefix) || b.similarity - a.similarity || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
    .map((s) => s.row);
}
