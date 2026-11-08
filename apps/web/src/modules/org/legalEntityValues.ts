// Validation shared by the add and copy legal entity forms.
const codePattern = /^[A-Za-z0-9]{2,8}$/;
export type LegalEntityErrors = Partial<Record<"code" | "name" | "country" | "currency" | "timeZone", string>>;

export interface LegalEntityValues {
  code: string;
  name: string;
  country: string;
  currency: string;
  timeZone: string;
}

export function validateLegalEntity({ code, name, country, currency, timeZone }: LegalEntityValues): LegalEntityErrors {
  const errors: LegalEntityErrors = {};
  if (!codePattern.test(code)) {
    errors.code = "Use 2 to 8 letters or digits.";
  }
  if (name.trim() === "" || name.trim().length > 120) {
    errors.name = "Enter a name of 1 to 120 characters.";
  }
  if (country === "") {
    errors.country = "Select a country.";
  }
  if (currency === "") {
    errors.currency = "Select an accounting currency.";
  }
  if (timeZone.trim() === "") {
    errors.timeZone = "Enter an IANA time zone such as America/Detroit.";
  }
  return errors;
}

export const codeProblems: Record<string, string> = {
  "ORG-LE-CODE-DUPLICATE": "A legal entity with this code already exists.",
  "ORG-LE-CODE-RESERVED": "This code is reserved. Choose another.",
};
