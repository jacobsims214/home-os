#!/usr/bin/env node
// ui_channel.mjs — drive the HOME OS UI at 390x844 and report what it RENDERED.
//
//   node ui_channel.mjs --persona amber --path /dashboard/bills --evidence-dir DIR
//   node ui_channel.mjs --persona amber --path /dashboard/bills --expect "Streamly"
//   node ui_channel.mjs --persona amber --path /dashboard/bills --json
//
// Prints ONE JSON object: { ok, persona, url, final_url, status, viewport, signed_in,
//   text, match, screenshot, console_errors, detail, elapsed_ms }
//
// ## Why CDP and not `chrome --dump-dom`
//
// `--dump-dom` renders without a session, so every page is the Dex login screen and
// the only honest answer it can give is "could not tell". A round's UI half has to see
// the page a signed-in parent sees. This drives Chrome over the DevTools protocol:
// sets the `home-os-token` cookie (the httpOnly Dex id_token `dex_login.py` minted),
// navigates, waits for the app shell, and reads `document.body.innerText` — the text a
// person would read — plus a 390x844 screenshot.
//
// ## The one rule (standing instructions §7)
//
// This script never reports a page it did not render. A login redirect, an empty shell,
// a console error storm, a navigation that never settled: each of those is `ok:false`
// with a `detail` naming it. `ok:true` means the page rendered signed-in AND (when
// --expect is given) the expected text was in it. There is no "probably fine".
//
// ## Agent width
//
// run.sh caps AGENT turns at 2 wide (R2: 5-wide degrades results). UI drivers are not
// agent turns and may be wider; this script is one browser per scenario, so the round
// runs as many as it likes — see run.sh `UI_JOBS`.

import { spawn } from "node:child_process"
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const VIEWPORT = { width: 390, height: 844 }

function arg(name, dflt = "") {
  const i = process.argv.indexOf("--" + name)
  if (i === -1) return dflt
  const v = process.argv[i + 1]
  return v && !v.startsWith("--") ? v : true
}
const tax = (name) => process.argv.includes("--" + name)

const persona = arg("persona", "")
const path = arg("path", "/dashboard")
const expectText = arg("expect", "")
const evidenceDir = arg("evidence-dir", "")
const asJson = tax("json")
const timeoutMs = Number(process.env.WARGAME_UI_TIMEOUT_MS || 45000)
const settleMs = Number(process.env.WARGAME_UI_SETTLE_MS || 2500)

const started = Date.now()
const out = {
  ok: false, persona, url: "", final_url: "", status: null,
  viewport: `${VIEWPORT.width}x${VIEWPORT.height}`, signed_in: false,
  text: "", match: null, screenshot: null, console_errors: [], detail: "", elapsed_ms: 0,
}

function finish(code, detail) {
  // `ok` is derived from the exit code, never set separately: two ways to say the same
  // thing is how they drift, and a JSON body that says ok:true while the process exits
  // 2 is exactly the "silently fails closed" shape §7 forbids. 0 = rendered and matched;
  // 1 = rendered but the expected fact was absent (a finding about the product);
  // 2 = could not tell (login screen, empty shell, timeout) — never a pass.
  out.ok = code === 0
  out.exit_code = code
  if (detail) out.detail = detail
  out.elapsed_ms = Date.now() - started
  if (evidenceDir) {
    try {
      mkdirSync(evidenceDir, { recursive: true })
      writeFileSync(join(evidenceDir, "ui-channel.json"), JSON.stringify(out, null, 1))
    } catch { /* evidence is best-effort; the verdict is not */ }
  }
  process.stdout.write(JSON.stringify(out) + "\n")
  process.exit(code)
}

const chrome = process.env.WARGAME_CHROME ||
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
if (!existsSync(chrome)) finish(2, "no Chrome/Chromium found; set WARGAME_CHROME")

// The token file is written 0600 by dex_login.py. Reading it here (rather than taking
// the token on argv) is what keeps it out of `ps` and out of any log.
const tokenFile = process.env.WARGAME_TOKEN_FILE ||
  (() => {
    const i = process.argv.indexOf("--token-file")
    return i === -1 ? "" : process.argv[i + 1]
  })()
if (!tokenFile || !existsSync(tokenFile)) {
  finish(2, "no WARGAME_TOKEN_FILE (or --token-file); run dex_login.py --persona <p> first")
}
let token = ""
try {
  token = readFileSync(tokenFile, "utf8").trim()
} catch (e) {
  finish(2, `could not read the token file: ${e.message}`)
}
if (!token) finish(2, "the token file is empty")

const site = (process.env.HOMEOS_SITE || "https://homeos.simstech.cloud").replace(/\/+$/, "")
const url = /^https?:\/\//.test(path) ? path : site + (path.startsWith("/") ? path : "/" + path)
out.url = url

const profile = process.env.WARGAME_CHROME_PROFILE || mkdtempSync(join(tmpdir(), "wg-ui-"))
const browser = spawn(chrome, [
  "--headless=new", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
  "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check",
  `--window-size=${VIEWPORT.width},${VIEWPORT.height}`,
  "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank",
], { stdio: ["ignore", "ignore", "pipe"] })

const killBrowser = () => { try { browser.kill("SIGKILL") } catch { /* gone */ } }
const hardStop = setTimeout(() => finish(2, `UI channel timed out after ${timeoutMs}ms`), timeoutMs)
hardStop.unref?.()

function wsUrl() {
  return new Promise((ok, fail) => {
    let buf = ""
    const onData = (d) => {
      buf += d
      const m = buf.match(/DevTools listening on (ws:\/\/\S+)/)
      if (m) { browser.stderr.off("data", onData); ok(m[1]) }
      else if (buf.length > 20000) { buf = buf.slice(-4000) }
    }
    browser.stderr.on("data", onData)
    browser.on("exit", (code) => fail(new Error(`Chrome exited (${code}): ${buf.slice(-300)}`)))
    browser.on("error", (e) => fail(new Error(`Chrome would not start: ${e.message}`)))
  })
}

let ws, sessionId, seq = 0
const pending = new Map()
function send(method, params = {}, sid = sessionId) {
  return new Promise((ok, fail) => {
    const id = ++seq
    pending.set(id, { ok, fail })
    ws.send(JSON.stringify({ id, method, params, sessionId: sid }))
  })
}

async function main() {
  const browserWs = await wsUrl()
  ws = new WebSocket(browserWs)
  await new Promise((ok, fail) => {
    ws.addEventListener("open", ok, { once: true })
    ws.addEventListener("error", () => fail(new Error("DevTools socket refused")), { once: true })
  })
  ws.addEventListener("message", (e) => {
    let m
    try { m = JSON.parse(e.data) } catch { return }
    if (m.method === "Runtime.consoleAPICalled" && m.params?.type === "error") {
      const txt = (m.params.args || []).map((a) => a.value ?? a.description ?? "").join(" ")
      if (txt) out.console_errors.push(txt.slice(0, 300))
    }
    if (m.method === "Network.responseReceived") {
      const r = m.params?.response
      if (r && r.status >= 500) {
        // A 5xx on a build asset (_next/static) is a note, not a verdict: Next keeps
        // the page working off the HTML it already served, and a rolling deploy makes
        // one of these flicker. A 5xx on the DOCUMENT itself means the page did not
        // load, and that is worth blocking on.
        const asset = /\/_next\/static\//.test(r.url || "")
        out.console_errors.push(`${asset ? "ASSET " : ""}HTTP ${r.status} ${r.url}`.slice(0, 300))
      }
    }
    if (m.id && pending.has(m.id)) {
      const p = pending.get(m.id)
      pending.delete(m.id)
      m.error ? p.fail(new Error(`${m.error.message} (${m.error.code})`)) : p.ok(m.result)
    }
  })

  const { targetId } = await send("Target.createTarget", { url: "about:blank" }, undefined)
  const attached = await send("Target.attachToTarget", { targetId, flatten: true }, undefined)
  sessionId = attached.sessionId
  await send("Page.enable")
  await send("Runtime.enable")
  await send("Network.enable")
  await send("Emulation.setDeviceMetricsOverride", {
    width: VIEWPORT.width, height: VIEWPORT.height, deviceScaleFactor: 3, mobile: true,
  })
  // The cookie must be set on the SESSION (browser-level Network.setCookie does not
  // exist — the first version of this tried it and got "wasn't found"), before nav.
  const cookie = await send("Network.setCookie", {
    name: "home-os-token", value: token, url: site + "/", path: "/",
    secure: true, httpOnly: true, sameSite: "Lax",
  })
  if (!cookie?.success) {
    finish(2, "the browser refused the home-os-token cookie; cannot reach a signed-in page")
  }

  const nav = await send("Page.navigate", { url })
  if (nav?.errorText) finish(2, `navigation failed: ${nav.errorText}`)

  // Wait for the document to be interactive AND for the app to stop being the login
  // screen. A fixed sleep alone would race the SPA; polling alone would never settle.
  const deadline = Date.now() + timeoutMs - 5000
  let last = ""
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 600))
    try {
      const r = await send("Runtime.evaluate", {
        expression: "document.readyState + '|' + location.href + '|' + (document.body ? document.body.innerText.length : 0)",
        returnByValue: true,
      })
      last = r.result?.value || ""
      const [ready, href, len] = String(last).split("|")
      const loggedIn = !/\/dex\//.test(href)
      if (ready === "complete" && loggedIn && Number(len) > 40) break
      if (ready === "complete" && loggedIn && Date.now() > deadline - (timeoutMs / 2)) break
    } catch { /* the execution context is replaced mid-navigation; retry */ }
  }
  await new Promise((r) => setTimeout(r, settleMs))

  // One evaluate for everything the round reasons about, so agent/UI parity compares
  // one snapshot rather than several that drifted apart.
  const snap = await send("Runtime.evaluate", {
    expression: `(() => ({
      title: document.title,
      url: location.href,
      signed_in: !/\\/dex\\//.test(location.href),
      text: (document.body ? document.body.innerText : ""),
    }))()`,
    returnByValue: true,
  })
  const v = snap.result?.value || {}
  out.final_url = v.url || ""
  out.signed_in = !!v.signed_in
  out.text = String(v.text || "").replace(/\u00a0/g, " ").trim()

  if (!out.signed_in) {
    finish(2, `${url} redirected to the Dex login screen — no signed-in session ` +
             `(token for ${persona} is not accepted)`)
  }
  if (out.text.length < 20) {
    finish(2, `${url} rendered an empty shell (${out.text.length} chars of text); ` +
             `nothing was on screen to compare`)
  }

  if (evidenceDir) {
    try {
      mkdirSync(evidenceDir, { recursive: true })
      const shot = await send("Page.captureScreenshot", { format: "png" })
      const file = join(evidenceDir, `ui-${persona}.png`)
      writeFileSync(file, Buffer.from(shot.data, "base64"))
      out.screenshot = file
      writeFileSync(join(evidenceDir, `ui-${persona}.txt`), out.text)
    } catch (e) {
      out.console_errors.push(`screenshot failed: ${e.message}`)
    }
  }

  if (expectText) {
    // Case-insensitive substring on the rendered text. This is the whole parity
    // primitive: the round gives it the fact the agent reported and asks whether a
    // person looking at the phone would see it.
    const needle = String(expectText).toLowerCase()
    out.match = out.text.toLowerCase().includes(needle)
    if (!out.match) {
      finish(1, `rendered, but "${expectText}" is not in the page text`)
    }
  }

  const realErrors = out.console_errors.filter(
    (e) => !/favicon|manifest|sw\.js/i.test(e) && !/^ASSET /.test(e))
  if (realErrors.length) {
    // A console error storm is not a parity question, it is "the page did not render
    // cleanly". Say so instead of comparing against a half-rendered page.
    finish(2, `${realErrors.length} console/HTTP error(s) while rendering: ${realErrors[0]}`)
  }

  finish(0, expectText ? `rendered ${url} and found "${expectText}"` : `rendered ${url}`)
}

main().catch((e) => finish(2, `UI driver error: ${e.message}`))
  .finally(() => { clearTimeout(hardStop); killBrowser(); if (!process.env.WARGAME_CHROME_PROFILE) { try { rmSync(profile, { recursive: true, force: true }) } catch { /* temp dir */ } } })
