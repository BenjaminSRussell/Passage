import { createIntl, createIntlCache, type IntlShape } from "react-intl";
import { es } from "./es";
import { FUNCTION_MESSAGES } from "./functionMessages";
import { activeLocale, formatLocale, type Locale } from "./locale";
import { pseudoize } from "./pseudo";

const cache = createIntlCache();
const intls = new Map<string, IntlShape>();

/** One react-intl formatter per language, shared by the copy object and the provider. */
export function intlFor(locale: Locale): IntlShape {
  const tag = formatLocale(locale);
  let intl = intls.get(tag);
  if (!intl) {
    intl = createIntl({ locale: tag, messages: {}, onError: () => undefined }, cache);
    intls.set(tag, intl);
  }
  return intl;
}

const CATALOGS: Partial<Record<Locale, Readonly<Record<string, string>>>> = { es };

type Values = Record<string, string | number>;
const toValues = (params: readonly string[], args: readonly unknown[]): Values =>
  Object.fromEntries(params.map((name, i) => [name, typeof args[i] === "boolean" ? String(args[i]) : (args[i] as string | number)]));

/**
 * Returns a copy of the English `copy` object, with the same shape, in another language. English is returned as
 * it is. For a language with a catalog (Spanish), a string is its catalog message and a function formats its
 * catalog message with the function's arguments; anything the catalog lacks stays English. The pseudo-locale runs
 * the English text through `pseudoize`. Languages are fixed for the page, so this runs once, at import.
 */
export function localizeCopy<T extends object>(english: T, locale: Locale = activeLocale, statics: Values = {}): T {
  if (locale === "en") return english;
  const catalog = CATALOGS[locale];
  const intl = intlFor(locale);
  const walk = (node: unknown, path: string): unknown => {
    if (typeof node === "string") {
      if (locale === "en-XA") return pseudoize(node);
      const message = catalog?.[path];
      return message === undefined ? node : String(intl.formatMessage({ id: path, defaultMessage: message }, statics));
    }
    if (typeof node === "function") {
      const english = node as (...args: unknown[]) => string;
      if (locale === "en-XA") return (...args: unknown[]) => pseudoize(english(...args));
      const message = catalog?.[path];
      const spec = FUNCTION_MESSAGES[path];
      if (message === undefined || spec === undefined) return english;
      return (...args: unknown[]) => String(intl.formatMessage({ id: path, defaultMessage: message }, toValues(spec.params, args)));
    }
    if (node !== null && typeof node === "object") {
      return Object.fromEntries(Object.entries(node).map(([key, child]) => [key, walk(child, path === "" ? key : `${path}.${key}`)]));
    }
    return node;
  };
  return walk(english, "") as T;
}

/** Every leaf of a copy object by path: strings with their text, functions marked. Used by the tests and the extract script. */
export function leaves(node: unknown, path = ""): Map<string, string | "function"> {
  const out = new Map<string, string | "function">();
  const visit = (value: unknown, at: string) => {
    if (typeof value === "string") out.set(at, value);
    else if (typeof value === "function") out.set(at, "function");
    else if (value !== null && typeof value === "object") for (const [key, child] of Object.entries(value)) visit(child, at === "" ? key : `${at}.${key}`);
  };
  visit(node, path);
  return out;
}
