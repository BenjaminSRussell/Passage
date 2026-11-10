import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";

/**
 * Languages the web app is translated into. `en-XA` is the pseudo-locale: every shell string comes out accented,
 * padded and bracketed, so a string that is still hard-coded in English, or a layout that cuts longer text, shows up
 * at a glance and in the pseudo-locale Playwright pass. It is selected only with `?lang=en-XA`.
 */
export const LOCALES = ["en", "es", "en-XA"] as const;
export type Locale = (typeof LOCALES)[number];

/** Languages a person can choose; the pseudo-locale is for developers and CI. */
export const CHOOSABLE: readonly Locale[] = ["en", "es"];

export const LOCALE_KEY = "passage.locale.v1";

const isLocale = (value: string | null | undefined): value is Locale => LOCALES.includes(value as Locale);

/** `?lang=` wins (for the pseudo pass and for support), then the saved choice, then the browser language. */
export function resolveLocale(search: string, storage: PreferenceStorage, browserLanguages: readonly string[]): Locale {
  const asked = new URLSearchParams(search).get("lang");
  if (isLocale(asked)) return asked;
  const saved = storage.read(LOCALE_KEY);
  if (isLocale(saved) && saved !== "en-XA") return saved;
  for (const language of browserLanguages) {
    const base = language.toLowerCase().split("-")[0];
    if (base === "es") return "es";
    if (base === "en") return "en";
  }
  return "en";
}

export function saveLocale(locale: Locale, storage: PreferenceStorage = localPreferenceStorage): void {
  storage.write(LOCALE_KEY, locale);
}

function detect(): Locale {
  if (typeof window === "undefined") return "en";
  return resolveLocale(window.location.search, localPreferenceStorage, typeof navigator === "undefined" ? [] : (navigator.languages ?? [navigator.language]));
}

/** Fixed for the life of the page: changing language reloads, so strings read at import never go stale. */
export const activeLocale: Locale = detect();

/** The BCP 47 tag the browser's Intl formatters should use (the pseudo-locale formats as English). */
export const formatLocale = (locale: Locale = activeLocale): string => (locale === "en-XA" ? "en" : locale);
