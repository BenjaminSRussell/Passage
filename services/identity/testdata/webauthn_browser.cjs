// Drives Keycloak's browser login in Chromium with a CDP virtual WebAuthn authenticator, for
// TestKeycloakBrowserLoginWithWebAuthnSecondFactor (keycloak_webauthn_test.go). It reads one JSON
// command per stdin line and answers each with one JSON line on stdout, so the Go test can change
// the user through the admin API between logins while the authenticator keeps its credential.
//
// The browser reaches Keycloak under the realm's pinned WebAuthn RP ID host (start.rpHost), mapped
// to the real Keycloak host, so the ceremony runs against the exported policy rather than a test RP ID.
'use strict';

const crypto = require('node:crypto');
const readline = require('node:readline');
const { chromium } = require('playwright');

let browser, context, page, cdp, authenticatorId, callbackPrefix, callback;
let totp = { secret: null, last: 0 };
const otpPeriod = 30;

function totpCode(secret, step) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const sum = crypto.createHmac('sha1', secret).update(msg).digest();
  const offset = sum[sum.length - 1] & 0x0f;
  return String((sum.readUInt32BE(offset) & 0x7fffffff) % 1000000).padStart(6, '0');
}

// nextCode returns a code for a time step not used before, within the realm's one-step look-ahead.
async function nextCode() {
  for (;;) {
    const now = Math.floor(Date.now() / 1000 / otpPeriod);
    const step = Math.max(now, totp.last + 1);
    if (step <= now + 1) {
      totp.last = step;
      return totpCode(totp.secret, step);
    }
    await new Promise((r) => setTimeout(r, (now + 1) * otpPeriod * 1000 - Date.now() + 100));
  }
}

async function start(cmd) {
  const origin = new URL(cmd.origin);
  callbackPrefix = cmd.callbackPrefix;
  // The full Chromium build: the headless shell ignores --unsafely-treat-insecure-origin-as-secure,
  // and WebAuthn needs a secure context (the test talks plain http to start-dev).
  browser = await chromium.launch({
    channel: 'chromium',
    args: [
      `--host-resolver-rules=MAP ${origin.hostname} ${cmd.mapTo}`,
      `--unsafely-treat-insecure-origin-as-secure=${origin.origin}`,
      '--no-proxy-server',
    ],
  });
  context = await browser.newContext();
  // Keycloak redirects to passage-web's callback, which does not resolve: the request is recorded
  // (a route would not see a server redirect) and the browser's error page is never read.
  context.on('request', (request) => {
    if (request.url().startsWith(callbackPrefix)) callback = request.url();
  });
  page = await context.newPage();
  page.on('dialog', (dialog) => dialog.accept('ci-virtual-authenticator'));
  cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  ({ authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2', transport: 'internal', hasResidentKey: true,
      hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
    },
  }));
  return { browser: browser.version() };
}

async function settle(action) {
  await Promise.all([page.waitForNavigation({ timeout: 20000 }).catch(() => {}), action()]);
  await page.waitForLoadState('load').catch(() => {});
}

// has retries while a navigation replaces the page under the query (a form the page's own script
// submits, such as the WebAuthn ones after the ceremony).
async function has(selector) {
  for (let attempt = 0; ; attempt++) {
    try {
      return (await page.$(selector)) !== null;
    } catch (err) {
      if (attempt >= 10 || !/context was destroyed|navigat/i.test(String(err))) throw err;
      await page.waitForLoadState('load').catch(() => {});
      await new Promise((r) => setTimeout(r, 200));
    }
  }
}

// login answers Keycloak's pages until passage-web's callback is requested. prefer "webauthn"
// takes "Try another way" from the OTP form to the passkey; prefer "otp" answers the OTP form.
async function login(cmd) {
  callback = undefined;
  const steps = [];
  let passwordForms = 0;
  let triedAnother = false;
  await page.goto(cmd.url);
  for (let i = 0; i < 12 && !callback; i++) {
    if (await has('#totpSecret')) {
      totp = { secret: Buffer.from(await page.inputValue('#totpSecret'), 'utf8'), last: 0 };
      await page.fill('#totp', await nextCode());
      await page.fill('#userLabel', 'ci');
      steps.push('enrol-totp');
      await settle(() => page.click('#saveTOTPBtn'));
    } else if (await has('#registerWebAuthn')) {
      steps.push('register-webauthn');
      await settle(() => page.click('#registerWebAuthn'));
    } else if (await has('#authenticateWebAuthnButton')) {
      if (cmd.prefer === 'otp' && !triedAnother && (await has('#try-another-way'))) {
        triedAnother = true;
        steps.push('try-another-way');
        await settle(() => page.click('#try-another-way'));
        continue;
      }
      steps.push('webauthn');
      await settle(() => page.click('#authenticateWebAuthnButton'));
    } else if (await has('#kc-otp-login-form')) {
      if (cmd.prefer === 'webauthn' && !triedAnother && (await has('#try-another-way'))) {
        triedAnother = true;
        steps.push('try-another-way');
        await settle(() => page.click('#try-another-way'));
        continue;
      }
      if (!totp.secret) return { error: 'OTP form shown before TOTP enrolment', steps };
      await page.fill('#otp', await nextCode());
      steps.push('otp');
      await settle(() => page.click('#kc-login'));
    } else if (await has('#kc-select-credential-form')) {
      const want = cmd.prefer === 'otp' ? /authenticator application/i : /passkey/i;
      const item = page.locator('.pf-v5-c-data-list__item, .select-auth-box-parent, [onclick*="requestSubmit"]').filter({ hasText: want }).first();
      if ((await item.count()) === 0) return { error: 'no matching authenticator on the selection page', steps, page: (await page.innerText('body')).slice(0, 600) };
      steps.push('select-' + (cmd.prefer || 'webauthn'));
      await settle(() => item.click());
    } else if (await has('#kc-error-credential-form')) {
      steps.push('webauthn-error');
      return { refused: true, steps, page: (await page.innerText('body')).slice(0, 600) };
    } else if (await has('#kc-form-login')) {
      if (++passwordForms > 1) return { refused: true, steps, page: (await page.innerText('body')).slice(0, 600) };
      await page.fill('#username', cmd.username);
      await page.fill('#password', cmd.password);
      steps.push('password');
      await settle(() => page.click('#kc-login'));
    } else {
      return { error: 'no page this driver knows how to answer', steps, url: page.url(), page: (await page.innerText('body').catch(() => '')).slice(0, 600) };
    }
  }
  if (!callback) return { error: 'login did not reach the callback', steps, url: page.url() };
  // Let the navigation to the unresolvable callback end on Chromium's error page, so the next
  // login's navigation is not interrupted by it.
  await page.waitForURL((u) => u.protocol === 'chrome-error:', { timeout: 10000 }).catch(() => {});
  await page.waitForLoadState('load').catch(() => {});
  return { callback, steps };
}

async function run(cmd) {
  switch (cmd.cmd) {
    case 'start':
      return start(cmd);
    case 'login':
      return login(cmd);
    case 'forget-session':
      await context.clearCookies();
      return {};
    case 'credentials': {
      const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
      return { count: credentials.length, rpIds: credentials.map((c) => c.rpId) };
    }
    case 'user-verified':
      await cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified: cmd.value });
      return {};
    case 'close':
      await browser?.close();
      return {};
    default:
      return { error: 'unknown command ' + cmd.cmd };
  }
}

const lines = readline.createInterface({ input: process.stdin });
let queue = Promise.resolve();
lines.on('line', (line) => {
  queue = queue.then(async () => {
    let out;
    try {
      out = await run(JSON.parse(line));
    } catch (err) {
      out = { error: String(err && err.stack ? err.stack : err) };
    }
    process.stdout.write(JSON.stringify(out) + '\n');
  });
});
lines.on('close', () => {
  queue.then(() => browser?.close()).finally(() => process.exit(0));
});
