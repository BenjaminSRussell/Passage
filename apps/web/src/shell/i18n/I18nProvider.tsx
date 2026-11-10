import { useEffect, type ReactNode } from "react";
import { IntlProvider } from "react-intl";
import { activeLocale, formatLocale } from "./locale";

/**
 * Gives react-intl components (FormattedMessage, useIntl) the active language and sets the page's lang
 * attribute, which screen readers use to pick a voice. Shell strings come from `copy`, which is already in the
 * active language; pages that use react-intl directly add their messages here as they are extracted.
 */
export function I18nProvider({ children }: { children: ReactNode }) {
  useEffect(() => {
    document.documentElement.lang = formatLocale(activeLocale);
    document.documentElement.dir = "ltr";
  }, []);
  return (
    <IntlProvider locale={formatLocale(activeLocale)} defaultLocale="en" messages={{}} onError={() => undefined}>
      {children}
    </IntlProvider>
  );
}
