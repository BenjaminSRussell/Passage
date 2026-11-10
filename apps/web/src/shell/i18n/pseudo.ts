const MAP: Record<string, string> = {
  a: "à", b: "ƀ", c: "ç", d: "ð", e: "é", f: "ƒ", g: "ĝ", h: "ĥ", i: "î", j: "ĵ", k: "ķ", l: "ļ", m: "ɱ", n: "ñ", o: "ö", p: "þ", q: "ǫ",
  r: "ŕ", s: "š", t: "ţ", u: "û", v: "ṽ", w: "ŵ", x: "ẋ", y: "ý", z: "ž",
  A: "À", B: "Ɓ", C: "Ç", D: "Ð", E: "É", F: "Ƒ", G: "Ĝ", H: "Ĥ", I: "Î", J: "Ĵ", K: "Ķ", L: "Ļ", M: "Ṁ", N: "Ñ", O: "Ö", P: "Þ", Q: "Ǫ",
  R: "Ŕ", S: "Š", T: "Ţ", U: "Û", V: "Ṽ", W: "Ŵ", X: "Ẋ", Y: "Ý", Z: "Ž",
};

/**
 * Pseudo-localizes a formatted message: letters become accented look-alikes, the text grows by about a third (the
 * usual growth of a translation) and is wrapped in brackets so truncation is visible. Digits and punctuation stay,
 * so numbers and dates in a message still read correctly.
 */
export function pseudoize(text: string): string {
  const accented = Array.from(text, (ch) => MAP[ch] ?? ch).join("");
  const letters = (text.match(/\p{L}/gu) ?? []).length;
  return `[${accented}${"~".repeat(Math.ceil(letters * 0.3))}]`;
}

/** True when a visible string went through pseudoize. */
export const isPseudo = (text: string): boolean => text.startsWith("[") && text.endsWith("]") && /[~à-žÀ-Ž]/.test(text);
