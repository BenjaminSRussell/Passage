import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { ApiError, publicPost } from "../api";
import { Field, messageOf, Notice } from "../ui";

/** The pages anyone can open: they have no session, so they sit outside the signed-in shell. */
function PublicFrame({ title, children }: { title: string; children: ReactNode }) {
  useEffect(() => {
    document.getElementById("page-title")?.focus();
  }, []);
  return (
    <>
      <header><strong>Passage Supplier Portal</strong><a href="/bff/login">Sign in</a></header>
      <main id="main">
        <section aria-labelledby="page-title">
          <h1 id="page-title" tabIndex={-1}>{title}</h1>
          {children}
        </section>
      </main>
    </>
  );
}

const COUNTRY_CODES = ["AR", "AT", "AU", "BE", "BR", "CA", "CH", "CL", "CN", "CO", "CZ", "DE", "DK", "ES", "FI", "FR", "GB", "GR", "HK", "HU", "IE", "IL", "IN", "IT", "JP", "KR", "MX", "MY", "NL", "NO", "NZ", "PH", "PL", "PT", "RO", "SE", "SG", "SK", "TH", "TR", "TW", "US", "VN", "ZA"];
const REGIONS = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(["en"], { type: "region" }) : undefined;
export const COUNTRIES: Array<[string, string]> = COUNTRY_CODES.map((c): [string, string] => [c, REGIONS?.of(c) ?? c]).sort((a, b) => a[1].localeCompare(b[1]));

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** The registration form a company fills in to become a supplier. The field `homepage` is a trap for bots and is never shown. */
export function Register() {
  const [open, setOpen] = useState<boolean>();
  const [form, setForm] = useState({ companyName: "", contactName: "", email: "", phone: "", country: "", taxId: "", website: "", message: "", homepage: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));

  useEffect(() => {
    let live = true;
    fetch("/bff/registration", { credentials: "same-origin" }).then((r) => r.json() as Promise<{ open: boolean }>).then((b) => live && setOpen(b.open), () => live && setOpen(false));
    return () => {
      live = false;
    };
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const gaps: Record<string, string> = {};
    if (!form.companyName.trim()) gaps["companyName"] = "Enter your company name.";
    if (!form.contactName.trim()) gaps["contactName"] = "Enter a contact name.";
    if (!EMAIL.test(form.email.trim())) gaps["email"] = "Enter an email address we can reach you on.";
    if (!form.country) gaps["country"] = "Choose your country.";
    setErrors(gaps);
    if (Object.keys(gaps).length > 0) return setError("Fix the fields marked below.");
    try {
      await publicPost("/bff/prospects", form);
      setDone(true);
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError("Too many registrations from this address. Try again later.");
      else setError(messageOf(err));
    }
  }

  if (done) {
    return <PublicFrame title="Thank you"><Notice kind="ok">We received your registration. A buyer will review it and contact {form.email.trim()}.</Notice></PublicFrame>;
  }
  if (open === false) return <PublicFrame title="Become a supplier"><p>Supplier registration is closed at the moment. If you were invited, use the link in your invitation.</p></PublicFrame>;
  const bad = (k: string) => (errors[k] ? true : undefined);
  return (
    <PublicFrame title="Become a supplier">
      <p>Tell us about your company. A buyer reviews every registration; you will hear from us by email.</p>
      <form onSubmit={submit} noValidate>
        <Field label="Company name">{(id) => <input id={id} value={form.companyName} maxLength={200} aria-invalid={bad("companyName")} onChange={set("companyName")} />}</Field>
        <Field label="Contact name">{(id) => <input id={id} value={form.contactName} maxLength={140} aria-invalid={bad("contactName")} onChange={set("contactName")} />}</Field>
        <Field label="Email address">{(id) => <input id={id} type="email" value={form.email} maxLength={254} aria-invalid={bad("email")} onChange={set("email")} />}</Field>
        <Field label="Phone (optional)">{(id) => <input id={id} type="tel" value={form.phone} maxLength={40} onChange={set("phone")} />}</Field>
        <Field label="Country">{(id) => <select id={id} value={form.country} aria-invalid={bad("country")} onChange={set("country")}><option value="">Choose</option>{COUNTRIES.map(([code, name]) => <option key={code} value={code}>{name}</option>)}</select>}</Field>
        <Field label="Tax or company registration number (optional)">{(id) => <input id={id} value={form.taxId} maxLength={40} onChange={set("taxId")} />}</Field>
        <Field label="Website (optional)">{(id) => <input id={id} type="url" value={form.website} maxLength={200} onChange={set("website")} />}</Field>
        <Field label="What do you supply?">{(id) => <textarea id={id} value={form.message} maxLength={2000} onChange={set("message")} />}</Field>
        <div className="hp" aria-hidden="true">
          <label>Leave this field empty<input tabIndex={-1} autoComplete="off" name="homepage" value={form.homepage} onChange={set("homepage")} /></label>
        </div>
        {Object.entries(errors).map(([k, v]) => <p key={k} className="error" role="alert">{v}</p>)}
        {error ? <Notice kind="error">{error}</Notice> : null}
        <button type="submit">Send registration</button>
      </form>
    </PublicFrame>
  );
}

interface InviteInfo { email: string; displayName: string; vendorName: string }

/** The page an invitation link opens: shows who invited whom, then creates the account. */
export function AcceptInvite() {
  // The token is in the fragment, so it is never sent to a server or kept in its logs; a link made before that carried it
  // in the query string.
  const location = useLocation();
  const token = new URLSearchParams(location.hash.replace(/^#/, "")).get("token") ?? new URLSearchParams(location.search).get("token") ?? "";
  const [info, setInfo] = useState<InviteInfo>();
  const [problem, setProblem] = useState("");
  const [name, setName] = useState("");
  const [result, setResult] = useState<{ provisioned: boolean }>();
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    if (!token) {
      setProblem("This invitation link is incomplete.");
      return;
    }
    publicPost<InviteInfo>("/bff/invites/verify", { token }).then(
      (i) => live && (setInfo(i), setName(i.displayName)),
      (e: unknown) => live && setProblem(e instanceof ApiError && e.status === 404 ? "This invitation is not valid, has expired, or was already used. Ask your administrator for a new one." : messageOf(e)),
    );
    return () => {
      live = false;
    };
  }, [token]);

  async function accept(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      setResult(await publicPost<{ provisioned: boolean }>("/bff/invites/accept", { token, displayName: name.trim() }));
    } catch (err) {
      setError(messageOf(err));
    }
  }

  if (problem) return <PublicFrame title="Invitation"><Notice kind="error">{problem}</Notice></PublicFrame>;
  if (result) {
    return (
      <PublicFrame title="You are in">
        <Notice kind="ok">{result.provisioned ? "Your account is ready. Sign in to continue." : "Your invitation is accepted. Your administrator finishes setting up your account, then you can sign in."}</Notice>
      </PublicFrame>
    );
  }
  if (!info) return <PublicFrame title="Invitation"><p role="status">Checking your invitation…</p></PublicFrame>;
  return (
    <PublicFrame title="Join your company's supplier portal">
      <p>You were invited to the supplier portal of <strong>{info.vendorName}</strong> as {info.email}.</p>
      <form onSubmit={accept} noValidate>
        <Field label="Your name">{(id) => <input id={id} value={name} maxLength={140} onChange={(e) => setName(e.target.value)} />}</Field>
        {error ? <Notice kind="error">{error}</Notice> : null}
        <button type="submit">Accept invitation</button>
      </form>
    </PublicFrame>
  );
}
