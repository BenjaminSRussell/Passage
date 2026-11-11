import { afterEach, describe, expect, it } from "vitest";
import { FakeProvisioner } from "../src/provision.ts";
import { FakeScanner } from "../src/scan.ts";
import { HONEYPOT_FIELD, MAX_PUBLIC_BODY_BYTES } from "../src/server.ts";
import { APP, LE_1, TENANT, VENDOR_A, api, signIn, startRig, tokenClaims, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

const INVITE_ID = "44444444-4444-4444-8444-444444444444";
const SECRET = "A".repeat(43);
const TOKEN = `inv1.${TENANT}.${LE_1}.${VENDOR_A}.${INVITE_ID}.${SECRET}`;
const registration = { tenantId: TENANT, legalEntityId: LE_1 };
const upload = (content: string) => ({ title: "Certificate", fileName: "iso.pdf", contentType: "application/pdf", contentBase64: Buffer.from(content).toString("base64") });
const claimsOf = (i: number) => tokenClaims(rig.calls[i]!.headers.get("x-passage-service-token") as string);

describe("invoice by received line", () => {
  it("lists invoiceable lines and submits an invoice for the session's vendor and legal entity", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    await api(rig, s, "GET", `/api/v1/invoiceable-lines?purchaseOrderId=${INVITE_ID}&vendorId=evil`);
    expect(rig.calls[0]!.url).toBe(`http://receiving.test/v1/receiving/vendor/invoiceable-lines?purchaseOrderId=${INVITE_ID}`);
    const lines = [{ receiptId: INVITE_ID, receiptLineNo: 1, qty: "2", unitPrice: "2.5" }];
    const res = await api(rig, s, "POST", "/api/v1/invoice-submissions", { invoiceNumber: "INV-1", invoiceDate: "2026-03-05", currency: "USD", lines, vendorId: "evil", legalEntityId: "evil" });
    expect(res.status).toBe(200);
    const call = rig.calls[1]!;
    expect(call.url).toBe("http://receiving.test/v1/receiving/vendor/invoice-submissions");
    expect(JSON.parse(call.body)).toEqual({ invoiceNumber: "INV-1", invoiceDate: "2026-03-05", currency: "USD", lines, legalEntityId: LE_1 });
    expect(claimsOf(1)).toMatchObject({ aud: "receiving", vnd: VENDOR_A, duties: ["receiving.vendor-portal.asn", "receiving.vendor-portal.invoice"] });
    await api(rig, s, "POST", `/api/v1/invoice-submissions/${INVITE_ID}/withdraw`);
    expect(rig.calls[2]!.url).toBe(`http://receiving.test/v1/receiving/vendor/invoice-submissions/${INVITE_ID}:withdraw`);
  });
});

describe("payment status", () => {
  it("reads the vendor's payments from payables with a payables token", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", "/api/v1/payments?status=PAID&limit=20&vendorId=evil");
    expect(res.status).toBe(200);
    expect(rig.calls[0]!.url).toBe("http://payables.test/v1/payables/vendor/payments?limit=20&status=PAID");
    expect(claimsOf(0)).toMatchObject({ aud: "payables", vnd: VENDOR_A, duties: ["payables.vendor-portal.read"] });
    expect((await api(rig, s, "POST", "/api/v1/payments", {})).status).toBe(404);
  });
});

describe("profile changes", () => {
  it("forwards the requested changes to party", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    await api(rig, s, "POST", "/api/v1/requests", { kind: "PROFILE_CHANGE", subject: "Rename", details: "d", changes: { legalName: "Acme Industrial" } });
    expect(JSON.parse(rig.calls[0]!.body)).toEqual({ kind: "PROFILE_CHANGE", subject: "Rename", details: "d", changes: { legalName: "Acme Industrial" } });
    expect(claimsOf(0)["duties"]).toEqual(["party.vendor-portal.read", "party.vendor-portal.write"]);
  });
});

describe("document virus scan", () => {
  it("refuses every upload when no scanner is configured and unscanned uploads are not allowed", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await api(rig, s, "POST", "/api/v1/documents", upload("%PDF"));
    expect(res.status).toBe(503);
    expect(((await res.json()) as { code: string }).code).toBe("SUP-BFF-503");
    expect(rig.calls).toHaveLength(0);
  });

  it("accepts an unscanned upload only when that is allowed on purpose", async () => {
    rig = await startRig({ scan: { allowUnscanned: true } });
    const s = await signIn(rig);
    expect((await api(rig, s, "POST", "/api/v1/documents", upload("%PDF"))).status).toBe(200);
    expect(rig.calls).toHaveLength(1);
  });

  it("scans the decoded bytes and forwards a clean upload", async () => {
    const scanner = new FakeScanner();
    rig = await startRig({ scan: { scanner, allowUnscanned: false } });
    const s = await signIn(rig);
    expect((await api(rig, s, "POST", "/api/v1/documents", upload("%PDF-1.4 hello"))).status).toBe(200);
    expect(scanner.seen.map((b) => b.toString())).toEqual(["%PDF-1.4 hello"]);
    expect(rig.calls).toHaveLength(1);
  });

  it("rejects an infected upload with 422 and sends nothing on", async () => {
    const scanner = new FakeScanner((c) => (c.toString().includes("EICAR") ? { clean: false, signature: "Eicar-Test-Signature" } : { clean: true }));
    rig = await startRig({ scan: { scanner, allowUnscanned: true } });
    const s = await signIn(rig);
    const res = await api(rig, s, "POST", "/api/v1/documents", upload("X5O!P%@AP EICAR"));
    expect(res.status).toBe(422);
    expect(((await res.json()) as { code: string; detail: string }).detail).toContain("virus");
    expect(rig.calls).toHaveLength(0);
  });

  it("refuses when the scanner cannot answer, even if unscanned uploads are allowed", async () => {
    rig = await startRig({ scan: { scanner: new FakeScanner(() => new Error("clamd down")), allowUnscanned: true } });
    const s = await signIn(rig);
    expect((await api(rig, s, "POST", "/api/v1/documents", upload("%PDF"))).status).toBe(503);
    expect(rig.calls).toHaveLength(0);
  });

  it("scans an invoice document before it goes to receiving, and refuses an infected one", async () => {
    const scanner = new FakeScanner((c) => (c.toString().includes("EICAR") ? { clean: false, signature: "Eicar-Test-Signature" } : { clean: true }));
    rig = await startRig({ scan: { scanner, allowUnscanned: false } });
    const s = await signIn(rig);
    const invoice = (content: string) => ({ format: "OCR", mediaType: "application/pdf", content: Buffer.from(content).toString("base64") });
    expect((await api(rig, s, "POST", "/api/v1/invoice-documents", invoice("%PDF-1.4 invoice"))).status).toBe(200);
    expect(rig.calls[0]!.url).toBe("http://receiving.test/v1/receiving/vendor/invoice-captures");
    expect((await api(rig, s, "POST", "/api/v1/invoice-documents", invoice("X5O!P%@AP EICAR"))).status).toBe(422);
    expect(scanner.seen.map((b) => b.toString())).toEqual(["%PDF-1.4 invoice", "X5O!P%@AP EICAR"]);
    expect(rig.calls).toHaveLength(1);
  });

  it("refuses an invoice document when no scanner is configured and unscanned uploads are not allowed", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await api(rig, s, "POST", "/api/v1/invoice-documents", { format: "UBL", mediaType: "application/xml", content: Buffer.from("<Invoice/>").toString("base64") });
    expect(res.status).toBe(503);
    expect(rig.calls).toHaveLength(0);
  });

  it("refuses an upload that is not a base64 string instead of forwarding it unscanned", async () => {
    const scanner = new FakeScanner();
    rig = await startRig({ scan: { scanner, allowUnscanned: true } });
    const s = await signIn(rig);
    const bytes = [...Buffer.from("X5O!P%@AP EICAR")];
    for (const content of [bytes, { 0: 88 }, 42, null]) {
      const res = await api(rig, s, "POST", "/api/v1/invoice-documents", { format: "OCR", mediaType: "application/pdf", content });
      expect(res.status).toBe(400);
      expect(((await res.json()) as { code: string }).code).toBe("SUP-BFF-400");
    }
    expect((await api(rig, s, "POST", "/api/v1/documents", { ...upload("%PDF"), contentBase64: bytes })).status).toBe(400);
    expect(scanner.seen).toHaveLength(0);
    expect(rig.calls).toHaveLength(0);
  });

  it("does not scan routes that carry no upload", async () => {
    const scanner = new FakeScanner();
    rig = await startRig({ scan: { scanner, allowUnscanned: false } });
    const s = await signIn(rig);
    await api(rig, s, "POST", "/api/v1/requests", { kind: "OTHER", subject: "s", details: "d" });
    expect(scanner.seen).toHaveLength(0);
  });
});

describe("vendor-admin invitations", () => {
  it("are for a vendor administrator only", async () => {
    rig = await startRig();
    const user = await signIn(rig);
    for (const [method, path, body] of [["GET", "/api/v1/invites"], ["POST", "/api/v1/invites", { email: "a@b.example" }], ["POST", `/api/v1/invites/${INVITE_ID}/revoke`]] as const) {
      expect((await api(rig, user, method, path, body)).status, `${method} ${path}`).toBe(403);
    }
    expect(rig.calls).toHaveLength(0);
    await rig.close();
    rig = await startRig();
    rig.claims["roles"] = ["supplier-admin"];
    const admin = await signIn(rig);
    rig.answer(Response.json({ invite: { id: INVITE_ID }, token: TOKEN }, { status: 201 }));
    const res = await api(rig, admin, "POST", "/api/v1/invites", { email: "c@acme.example", displayName: "Casey" });
    expect(res.status).toBe(201);
    const body = (await res.json()) as { token: string; acceptUrl: string };
    expect(body.acceptUrl).toBe(`${APP}/accept-invite#token=${encodeURIComponent(TOKEN)}`);
    expect(rig.calls[0]!.url).toBe("http://party.test/v1/party/vendor-portal/invites");
    expect(claimsOf(0)).toMatchObject({ aud: "party", vnd: VENDOR_A });
    // Party requires the vendor-admin duty on these routes; the token carries it and nothing else.
    expect(claimsOf(0)["duties"]).toEqual(["party.vendor-portal.admin"]);
    expect((await api(rig, admin, "GET", "/api/v1/invites")).status).toBe(200);
    expect((await api(rig, admin, "POST", `/api/v1/invites/${INVITE_ID}/revoke`)).status).toBe(200);
    expect(rig.calls[2]!.url).toBe(`http://party.test/v1/party/vendor-portal/invites/${INVITE_ID}:revoke`);
    expect([claimsOf(1)["duties"], claimsOf(2)["duties"]]).toEqual([["party.vendor-portal.admin"], ["party.vendor-portal.admin"]]);
    // The administrator's other calls carry the ordinary supplier duties, never the admin one.
    await api(rig, admin, "GET", "/api/v1/requests");
    expect(claimsOf(3)["duties"]).toEqual(["party.vendor-portal.read", "party.vendor-portal.write"]);
  });
});

const post = (path: string, body: unknown, headers: Record<string, string> = {}) =>
  fetch(`${rig.base}${path}`, { method: "POST", headers: { "content-type": "application/json", ...headers }, body: typeof body === "string" ? body : JSON.stringify(body) });

describe("accepting an invitation", () => {
  const details = { email: "c@acme.example", displayName: "Casey", vendorName: "Acme Valves" };

  it("verifies, creates the user, then spends the invitation, with tokens that carry only the accept duty", async () => {
    const provisioner = new FakeProvisioner();
    rig = await startRig({ provisioner });
    rig.answers(Response.json(details), Response.json({ ...details, status: "ACCEPTED" }));
    const res = await post("/bff/invites/accept", { token: TOKEN, displayName: " Casey Q " });
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ status: "ACCEPTED", provisioned: true, email: "c@acme.example" });
    expect(rig.calls.map((c) => c.url)).toEqual([
      `http://party.test/v1/party/vendor-portal/invites/${INVITE_ID}:verify`,
      `http://party.test/v1/party/vendor-portal/invites/${INVITE_ID}:accept`,
    ]);
    expect(provisioner.users).toEqual([{ email: "c@acme.example", displayName: "Casey Q", tenantId: TENANT, legalEntityId: LE_1, vendorId: VENDOR_A, role: "supplier-user" }]);
    for (let i = 0; i < 2; i++) {
      expect(claimsOf(i)).toMatchObject({ aud: "party", vnd: VENDOR_A, act: "invitee", duties: ["party.vendor-portal.invite-accept"] });
      expect(JSON.parse(rig.calls[i]!.body)).toEqual({ token: TOKEN });
    }
  });

  it("shows the invitation without creating anyone or spending it", async () => {
    const provisioner = new FakeProvisioner();
    rig = await startRig({ provisioner });
    rig.answer(Response.json(details));
    const res = await post("/bff/invites/verify", { token: TOKEN });
    expect(await res.json()).toEqual(details);
    expect(rig.calls).toHaveLength(1);
    expect(provisioner.users).toHaveLength(0);
  });

  it("does not spend the invitation when the user cannot be created", async () => {
    rig = await startRig({ provisioner: new FakeProvisioner(new Error("keycloak down")) });
    rig.answer(Response.json(details));
    const res = await post("/bff/invites/accept", { token: TOKEN });
    expect(res.status).toBe(502);
    expect(rig.calls).toHaveLength(1);
  });

  it("answers one way for a token that is malformed, unknown, expired or used", async () => {
    rig = await startRig();
    for (const token of ["nope", `inv1.${TENANT}.${LE_1}.${VENDOR_A}.${INVITE_ID}.short`, undefined, 7]) {
      expect((await post("/bff/invites/accept", { token })).status).toBe(404);
    }
    expect(rig.calls).toHaveLength(0);
    rig.answer(new Response(JSON.stringify({ code: "PTY-INVITE-INVALID" }), { status: 404 }));
    const res = await post("/bff/invites/accept", { token: TOKEN });
    expect(res.status).toBe(404);
    expect(await res.text()).not.toContain("PTY-INVITE-INVALID");
  });

  it("refuses a request from another origin, and limits attempts per address", async () => {
    rig = await startRig({ publicLimit: 2 });
    expect((await post("/bff/invites/verify", { token: TOKEN }, { origin: "https://evil.example.invalid" })).status).toBe(403);
    const statuses = [];
    for (let i = 0; i < 4; i++) statuses.push((await post("/bff/invites/verify", { token: "nope" }, { "x-forwarded-for": "198.51.100.7" })).status);
    expect(statuses).toEqual([404, 404, 429, 429]);
    // Another address has its own allowance, and the proxy's last entry is the one believed.
    expect((await post("/bff/invites/verify", { token: "nope" }, { "x-forwarded-for": "6.6.6.6, 198.51.100.8" })).status).toBe(404);
  });
});

describe("public prospect registration", () => {
  const form = { companyName: "Birch Valve Co", contactName: "Sam Birch", email: "sam@birch.example", country: "US", taxId: "12-3456789", message: "We make valves." };

  it("is closed unless a tenant and legal entity are configured", async () => {
    rig = await startRig();
    expect(await (await fetch(`${rig.base}/bff/registration`)).json()).toEqual({ open: false });
    expect((await post("/bff/prospects", form)).status).toBe(404);
    expect(rig.calls).toHaveLength(0);
  });

  it("sends the form to party with a vendorless token that carries only the register duty", async () => {
    rig = await startRig({ registration });
    expect(await (await fetch(`${rig.base}/bff/registration`)).json()).toEqual({ open: true });
    rig.answer(Response.json({ status: "RECEIVED" }, { status: 202 }));
    const res = await post("/bff/prospects", { ...form, vendorId: "evil", tenantId: "evil", unknown: "x" });
    expect(res.status).toBe(202);
    expect(await res.json()).toEqual({ status: "RECEIVED" });
    expect(rig.calls[0]!.url).toBe("http://party.test/v1/party/vendor-portal/prospects");
    expect(JSON.parse(rig.calls[0]!.body)).toEqual(form);
    const claims = claimsOf(0);
    expect(claims).toMatchObject({ iss: "supplier-portal", aud: "party", tid: TENANT, le: LE_1, duties: ["party.vendor-portal.register"] });
    expect(claims["vnd"]).toBeUndefined();
  });

  it("answers a filled honeypot like a registration and records nothing", async () => {
    rig = await startRig({ registration });
    const res = await post("/bff/prospects", { ...form, [HONEYPOT_FIELD]: "http://spam.example" });
    expect(res.status).toBe(202);
    expect(await res.json()).toEqual({ status: "RECEIVED" });
    expect(rig.calls).toHaveLength(0);
    // An empty honeypot is a person.
    rig.answer(Response.json({ status: "RECEIVED" }, { status: 202 }));
    expect((await post("/bff/prospects", { ...form, [HONEYPOT_FIELD]: "" })).status).toBe(202);
    expect(rig.calls).toHaveLength(1);
  });

  it("refuses a large body, a body that is not an object, text fields that are not text, and a foreign origin", async () => {
    rig = await startRig({ registration });
    expect((await post("/bff/prospects", { ...form, message: "x".repeat(MAX_PUBLIC_BODY_BYTES) })).status).toBe(413);
    expect((await post("/bff/prospects", "[1]")).status).toBe(400);
    expect((await post("/bff/prospects", "{nope")).status).toBe(400);
    expect((await post("/bff/prospects", { ...form, email: ["a@b.example"] })).status).toBe(400);
    expect((await post("/bff/prospects", form, { origin: "https://evil.example.invalid" })).status).toBe(403);
    expect(rig.calls).toHaveLength(0);
  });

  it("limits registrations per client address", async () => {
    rig = await startRig({ registration, publicLimit: 2 });
    rig.answers(Response.json({}, { status: 202 }), Response.json({}, { status: 202 }));
    const statuses = [];
    for (let i = 0; i < 3; i++) statuses.push((await post("/bff/prospects", form, { "x-forwarded-for": "203.0.113.9" })).status);
    expect(statuses).toEqual([202, 202, 429]);
    expect(rig.calls).toHaveLength(2);
  });

  it("passes party's validation problems through and never says sign in", async () => {
    rig = await startRig({ registration });
    rig.answer(new Response(JSON.stringify({ code: "PTY-SUP-INVALID", status: 422, errors: { email: "must be an email address" } }), { status: 422, headers: { "content-type": "application/problem+json" } }));
    const res = await post("/bff/prospects", { ...form, email: "nope" });
    expect(res.status).toBe(422);
    expect(((await res.json()) as { errors: Record<string, string> }).errors["email"]).toBeDefined();
    rig.answer(new Response("{}", { status: 401 }));
    expect((await post("/bff/prospects", form)).status).toBe(502);
  });
});
