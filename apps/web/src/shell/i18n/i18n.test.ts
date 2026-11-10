import { copyEnglish } from "../copy";
import { formatCurrency, formatNumber, formatPercent } from "../format";
import { es } from "./es";
import { FUNCTION_MESSAGES } from "./functionMessages";
import { LOCALE_KEY, resolveLocale } from "./locale";
import { intlFor, leaves, localizeCopy } from "./localize";
import { isPseudo, pseudoize } from "./pseudo";
import { problemsEs, translateProblem } from "./problemMessages";
import { problemCatalog } from "../errors/catalog";
import type { PreferenceStorage } from "../preferenceStorage";

const english = leaves(copyEnglish);
const storage = (value: string | null): PreferenceStorage => ({ read: () => value, write: () => true, remove: () => undefined });
const argNames = (message: string): string[] => [...new Set([...message.matchAll(/\{(\w+)(?:,|\})/g)].map((m) => m[1] ?? ""))].sort();

describe("message catalogs", () => {
  it("has an ICU message for every function in copy, and it gives what the English function gives", () => {
    const intl = intlFor("en");
    const missing: string[] = [];
    for (const [path, kind] of english) {
      if (kind !== "function") continue;
      const spec = FUNCTION_MESSAGES[path];
      if (!spec) {
        missing.push(path);
        continue;
      }
      const fn = path.split(".").reduce<unknown>((node, key) => (node as Record<string, unknown>)[key], copyEnglish) as (...args: unknown[]) => string;
      for (const args of spec.samples) {
        const values = Object.fromEntries(spec.params.map((p, i) => [p, typeof args[i] === "boolean" ? String(args[i]) : args[i]]));
        expect(String(intl.formatMessage({ id: path, defaultMessage: spec.en }, values as Record<string, string | number>)), `${path} ${JSON.stringify(args)}`).toBe(fn(...args));
      }
    }
    expect(missing).toEqual([]);
  });

  it("translates the shell into Spanish, with at least 95% of the strings covered", () => {
    const missing = [...english.keys()].filter((path) => es[path] === undefined);
    // A missing key falls back to English at runtime; this keeps the gap small and shows what it is.
    expect(missing.length / english.size, `missing: ${missing.join(", ")}`).toBeLessThan(0.05);
  });

  it("keeps every Spanish message's placeholders the same as the English one", () => {
    const bad: string[] = [];
    for (const [path, text] of Object.entries(es)) {
      const source = english.get(path);
      if (source === undefined) continue;
      const base = source === "function" ? FUNCTION_MESSAGES[path]?.en : source === "" ? "" : source;
      const expected = source === "function" ? argNames(base ?? "") : path === "home.description" ? ["shortcut"] : [];
      if (JSON.stringify(argNames(text)) !== JSON.stringify(expected)) bad.push(`${path}: ${argNames(text)} vs ${expected}`);
    }
    expect(bad).toEqual([]);
  });

  it("has no Spanish key that the English copy does not have, except removed pages", () => {
    const extra = Object.keys(es).filter((path) => !english.has(path) && !path.startsWith("notes.") && !path.startsWith("placeholder."));
    expect(extra).toEqual([]);
  });
});

describe("localizeCopy", () => {
  it("returns English untouched", () => {
    expect(localizeCopy(copyEnglish, "en")).toBe(copyEnglish);
  });

  it("gives Spanish strings, plural-aware functions and the shortcut in the home text", () => {
    const t = localizeCopy(copyEnglish, "es", { shortcut: "Ctrl K" });
    expect(t.nav.home).toBe("Inicio");
    expect(t.allFeatures.count(1)).toBe("1 función");
    expect(t.allFeatures.count(3)).toBe("3 funciones");
    expect(t.palette.noResults("tasa")).toBe("Ninguna página coincide con “tasa”.");
    expect(t.home.description).toContain("Ctrl K");
    expect(t.lists.loaded(3, "pedidos", true)).toBe("3 pedidos cargados, hay más disponibles");
    expect(t.myWork.summary(0, false)).toBe("No hay nada pendiente para usted.");
    expect(t.myWork.summary(1, false)).toBe("Hay 1 elemento pendiente para usted.");
    expect(t.myWork.summary(5, true)).toBe("Hay 5+ elementos pendientes para usted.");
    expect(t.format.days(2)).toBe("2 días");
  });

  it("pseudo-localizes every string and function result", () => {
    const t = localizeCopy(copyEnglish, "en-XA");
    for (const [path, kind] of leaves(t)) {
      if (kind === "function") continue;
      expect(isPseudo(kind), path).toBe(true);
    }
    expect(isPseudo(t.allFeatures.count(2))).toBe(true);
    expect(t.buttons.search).toBe(pseudoize("Search"));
  });
});

describe("pseudo-locale", () => {
  it("accents letters, grows the text and brackets it", () => {
    const out = pseudoize("Save changes");
    expect(out.startsWith("[") && out.endsWith("]")).toBe(true);
    expect(out).not.toContain("Save");
    expect(out.length).toBeGreaterThan("Save changes".length * 1.25);
    expect(pseudoize("12 of 40")).toContain("12");
  });
});

describe("locale choice", () => {
  it("prefers ?lang, then the saved choice, then the browser language", () => {
    expect(resolveLocale("?lang=en-XA", storage("es"), ["en-US"])).toBe("en-XA");
    expect(resolveLocale("", storage("es"), ["en-US"])).toBe("es");
    expect(resolveLocale("", storage(null), ["es-MX", "en"])).toBe("es");
    expect(resolveLocale("", storage(null), ["fr-FR"])).toBe("en");
    expect(resolveLocale("?lang=xx", storage(null), [])).toBe("en");
    // The pseudo-locale is never remembered.
    expect(resolveLocale("", storage("en-XA"), ["en"])).toBe("en");
    expect(LOCALE_KEY).toBe("passage.locale.v1");
  });
});

describe("problem catalog translation", () => {
  it("translates the common codes and keeps the English entry for the rest", () => {
    const code = "COM-0403";
    const entry = problemCatalog[code];
    if (!entry) throw new Error("catalog lost COM-0403");
    expect(translateProblem(code, entry, "es").message).toBe("No tiene permiso para hacer esto.");
    const other = Object.keys(problemCatalog).find((c) => !(c in problemsEs));
    if (other && problemCatalog[other]) expect(translateProblem(other, problemCatalog[other], "es")).toBe(problemCatalog[other]);
    expect(isPseudo(translateProblem(code, entry, "en-XA").message)).toBe(true);
  });

  it("translates only codes the catalog has", () => {
    expect(Object.keys(problemsEs).filter((code) => !(code in problemCatalog))).toEqual([]);
  });
});

describe("shared formatters", () => {
  it("format numbers, currency and percentages for the active language", () => {
    expect(formatNumber(1234.5)).toBe("1,234.5");
    expect(formatNumber("1234.50", { minimumFractionDigits: 2 })).toBe("1,234.50");
    expect(formatCurrency("1234.5", "USD")).toBe("$1,234.50");
    expect(formatCurrency(5, "xx")).toBe("");
    expect(formatPercent(0.256)).toBe("25.6%");
    expect(formatNumber("abc")).toBe("");
  });
});
