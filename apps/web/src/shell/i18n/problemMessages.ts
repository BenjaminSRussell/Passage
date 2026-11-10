import type { ProblemCatalogEntry } from "../errors/catalog";
import { activeLocale, type Locale } from "./locale";
import { pseudoize } from "./pseudo";

/**
 * Translations of the user-facing problem catalog (shell/errors/catalog.ts), keyed by problem code. The catalog
 * holds about 700 codes; the common ones every page can raise are translated first, and a code without a
 * translation shows its English entry. `translateProblem` is applied by `lookupProblem`.
 */
const TRY_LATER = "Vuelva a intentarlo en unos minutos. Si sigue ocurriendo, comuníquese con soporte con los detalles que aparecen abajo.";
const RELOAD_AND_REDO = "Vuelva a cargar la página para ver la última versión y haga su cambio de nuevo.";
const ASK_ADMIN = "Pida acceso a un administrador si lo necesita.";

export const problemsEs: Readonly<Record<string, ProblemCatalogEntry>> = {
  "COM-0000": { message: "Algo salió mal.", action: TRY_LATER },
  "COM-0001": { message: "Algunos de los datos que escribió no son válidos.", action: "Revise los campos marcados en el formulario, corríjalos y vuelva a intentarlo." },
  "COM-0002": { message: "A la solicitud le faltaba información que el servicio necesita para aceptarla de forma segura.", action: "Vuelva a cargar la página e inténtelo de nuevo." },
  "COM-0400": {
    message: "El servicio no pudo leer la solicitud.",
    action: "Vuelva a cargar la página e inténtelo de nuevo. Si sigue ocurriendo, comuníquese con soporte con los detalles que aparecen abajo.",
  },
  "COM-0403": { message: "No tiene permiso para hacer esto.", action: ASK_ADMIN },
  "COM-0404": { message: "No encontramos ese registro. Puede que se haya eliminado o que el enlace esté desactualizado.", action: "Vuelva a la lista y abra el registro de nuevo." },
  "COM-0409": { message: "Esta solicitud ya se envió una vez con datos distintos.", action: "Vuelva a cargar la página y empiece la acción de nuevo." },
  "COM-0410": { message: "La misma solicitud todavía se está procesando.", action: "Espere un momento y vuelva a cargar la página para ver el resultado." },
  "COM-0412": { message: "Otra persona cambió este registro después de que usted lo abrió.", action: RELOAD_AND_REDO },
  "COM-0413": { message: "La solicitud es demasiado grande para que el servicio la acepte.", action: "Envíe menos líneas o un archivo más pequeño e inténtelo de nuevo." },
  "COM-0428": { message: "Esta página está desactualizada, así que su cambio no se puede guardar de forma segura.", action: RELOAD_AND_REDO },
  "COM-0500": { message: "Algo salió mal de nuestro lado.", action: TRY_LATER },
  "COM-0502": { message: "Un servicio del que depende esta acción no respondió.", action: TRY_LATER },
  "COM-0503": { message: "Este servicio no está disponible en este momento.", action: TRY_LATER },
  "AUTH-0001": { message: "No se pudo confirmar su inicio de sesión. Puede que su sesión haya caducado.", action: "Inicie sesión de nuevo e inténtelo otra vez." },
  "AUTH-0002": { message: "No tiene el permiso que esta acción necesita.", action: ASK_ADMIN },
  "AUTH-0003": { message: "No tiene el permiso ni el acceso a la entidad legal que esta acción necesita.", action: ASK_ADMIN },
  "AUTH-0004": { message: "Este registro pertenece a una entidad legal a la que no tiene acceso.", action: ASK_ADMIN },
  "AUTH-0404": { message: "No encontramos ese registro, o no tiene acceso a él.", action: "Revise el enlace. Consulte a un administrador si debería poder verlo." },
  "HTTP-404": { message: "No encontramos lo que pidió.", action: "Vuelva a la lista y abra el registro de nuevo." },
  "HTTP-412": { message: "Otra persona cambió este registro después de que usted lo abrió.", action: RELOAD_AND_REDO },
  "HTTP-500": { message: "Algo salió mal de nuestro lado.", action: TRY_LATER },
  "HTTP-502": { message: "Un servicio del que depende esta página no respondió.", action: TRY_LATER },
  "HTTP-503": { message: "Este servicio no está disponible en este momento.", action: TRY_LATER },
};

/** The entry in the active language: the translation when there is one, the pseudo-localized English, or the English entry. */
export function translateProblem(code: string, entry: ProblemCatalogEntry, locale: Locale = activeLocale): ProblemCatalogEntry {
  if (locale === "es") return problemsEs[code] ?? entry;
  if (locale === "en-XA") return { message: pseudoize(entry.message), action: pseudoize(entry.action) };
  return entry;
}
