// Screenshot capture script for blog article
// Usage: node docs/blog-screenshots/capture.mjs

import { chromium } from 'playwright';
import { execSync } from 'child_process';
import { resolve } from 'path';

const BASE = 'http://localhost:8080';
const SCREENSHOT_DIR = resolve(import.meta.dirname);
const DB_PATH = resolve(import.meta.dirname, '../../efb-connector.db');
const EMAIL = 'demo@example.com';

async function getLatestMagicLinkToken() {
  // Query the DB for the most recent magic link token hash, then we need the raw token.
  // Since we can't reverse the hash, we'll generate the magic link via HTTP POST
  // and capture the token from the DB before/after.
  // Alternative: use sqlite3 to get the token hash, but we need the raw token.
  // In dev mode, the email isn't actually sent, but the token is stored.
  // We need to intercept it. Let's use a different approach:
  // POST to /login, then query the DB for the latest token hash and reconstruct.
  // Actually, we can't reverse SHA-256. Let's just write a small Go helper or
  // use the auth/verify endpoint differently.

  // Simplest: submit login form, check server logs for the magic link URL
  // In dev mode with placeholder Resend key, SendMagicLinkEmail will fail but
  // the token is already generated. We need to find it.
  //
  // Best approach: write token to a temp file in dev mode, or query DB.
  // Let's use a curl + grep approach on the server output.
  //
  // Actually simplest: just use the existing session from a previous login.
  // Or: generate token programmatically.
  return null; // Will use alternative approach
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
    locale: 'de-DE',
  });
  const page = await context.newPage();

  // Helper to screenshot
  async function shot(name, opts = {}) {
    const path = resolve(SCREENSHOT_DIR, name);
    await page.screenshot({ path, fullPage: opts.fullPage ?? false });
    console.log(`  Saved: ${name}`);
  }

  console.log('1. Landing page');
  await page.goto(BASE);
  await shot('01-landing.png', { fullPage: true });

  console.log('2. Login page');
  await page.goto(`${BASE}/login`);
  await shot('02-login.png');

  console.log('3. Login - fill email and submit');
  await page.fill('input[type="email"]', EMAIL);
  await shot('03-login-filled.png');

  // Submit the login form to show the "check your email" page
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
  await shot('04-login-sent.png');

  // Now get the magic link token from the database
  // The token is hashed in DB, but we can write a small Go program...
  // Instead, let's use a workaround: directly call the internal API
  // or query the DB to find the unhashed token.
  //
  // Since we can't reverse SHA-256, let's use a different approach:
  // Create a session directly by calling the Go server's auth endpoint
  // with a known token. In dev mode, we can generate a magic link via
  // the login form, and the token is returned... no, it's emailed.
  //
  // Simplest workaround: use sqlite3 in the script to create a session directly.
  console.log('4. Creating session directly via DB...');

  // Generate a session token and insert it into the DB
  const crypto = await import('crypto');
  const sessionToken = crypto.randomBytes(32).toString('base64url');
  const sessionHash = crypto.createHash('sha256').update(Buffer.from(sessionToken, 'base64url')).digest('hex');

  // Ensure user exists and get/create user ID
  try {
    execSync(`sqlite3 "${DB_PATH}" "INSERT OR IGNORE INTO users (email) VALUES ('${EMAIL}');"`, { stdio: 'pipe' });
  } catch(e) { /* user may already exist */ }

  const userId = execSync(`sqlite3 "${DB_PATH}" "SELECT id FROM users WHERE email='${EMAIL}';"`, { encoding: 'utf8' }).trim();

  // Create a session
  const expiresAt = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString().replace('T', ' ').replace('Z', '');
  execSync(`sqlite3 "${DB_PATH}" "INSERT INTO sessions (user_id, token_hash, expires_at) VALUES (${userId}, '${sessionHash}', '${expiresAt}');"`, { stdio: 'pipe' });

  // Set the session cookie
  await context.addCookies([{
    name: 'session',
    value: sessionToken,
    domain: 'localhost',
    path: '/',
    httpOnly: true,
    secure: false,
  }]);

  console.log('5. Dashboard (fresh user, no credentials)');
  await page.goto(`${BASE}/dashboard`);
  await page.waitForLoadState('networkidle');
  await shot('05-dashboard-setup.png', { fullPage: true });

  console.log('6. Garmin settings page');
  await page.goto(`${BASE}/settings/garmin`);
  await page.waitForLoadState('networkidle');
  await shot('06-garmin-settings.png');

  console.log('7. Garmin settings - fill credentials');
  await page.fill('input[name="email"]', 'paddler@example.com');
  await page.fill('input[name="password"]', '********');
  await shot('07-garmin-filled.png');

  // Submit Garmin credentials (mock provider in dev mode will accept)
  await page.locator('main button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
  await shot('08-garmin-saved.png');

  console.log('8. EFB settings page');
  await page.goto(`${BASE}/settings/efb`);
  await page.waitForLoadState('networkidle');
  await shot('09-efb-settings.png');

  console.log('9. EFB settings - fill credentials');
  await page.fill('input[name="username"]', 'mein-efb-name');
  await page.fill('input[name="password"]', '********');
  await shot('10-efb-filled.png');

  // Submit EFB credentials (mock provider in dev mode will accept)
  await page.locator('main button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
  await shot('11-efb-saved.png');

  console.log('10. Dashboard with setup wizard - preferences step');
  await page.goto(`${BASE}/dashboard`);
  await page.waitForLoadState('networkidle');

  // Check both preference checkboxes
  const autoCreateCheckbox = page.locator('input[name="auto_create_trips"]');
  const enrichCheckbox = page.locator('input[name="enrich_trips"]');
  if (await autoCreateCheckbox.count() > 0 && !(await autoCreateCheckbox.isChecked())) {
    await autoCreateCheckbox.check();
  }
  if (await enrichCheckbox.count() > 0 && !(await enrichCheckbox.isChecked())) {
    await enrichCheckbox.check();
  }
  await shot('12-dashboard-preferences.png', { fullPage: true });

  // Submit preferences
  const saveBtn = page.locator('main button:has-text("Speichern"), main button:has-text("Save")');
  if (await saveBtn.count() > 0) {
    await saveBtn.first().click();
    await page.waitForLoadState('networkidle');
  }

  console.log('11. Settings page (full view)');
  await page.goto(`${BASE}/settings`);
  await page.waitForLoadState('networkidle');
  await shot('13-settings.png', { fullPage: true });

  console.log('12. Trigger a sync');
  await page.goto(`${BASE}/dashboard`);
  await page.waitForLoadState('networkidle');

  // Check if there's a sync button and click it
  const syncBtn = page.locator('button:has-text("Jetzt synchronisieren"), button:has-text("Sync")');
  if (await syncBtn.count() > 0) {
    await syncBtn.first().click();
    await page.waitForTimeout(4000); // wait for sync to complete
    await page.waitForLoadState('networkidle');
  }
  await shot('14-dashboard-synced.png', { fullPage: true });

  console.log('13. Sync history');
  await page.goto(`${BASE}/sync/history`);
  await page.waitForLoadState('networkidle');
  await shot('15-sync-history.png', { fullPage: true });

  await browser.close();
  console.log('\nDone! All screenshots saved to docs/blog-screenshots/');
}

main().catch(err => {
  console.error('Error:', err);
  process.exit(1);
});
