// End-to-end: a real helper (a development build, tag devsink) against a
// local Stockpile — api AND worker running — printing to a fake raw label
// printer on this machine's own network address, and to the development
// file printer (a folder). Nothing reaches a real printer.
//
//   STOCKPILE=http://localhost:8071 ORIGIN=http://localhost:5191 \
//   OWNER_PASSWORD=… HELPER=path/to/stockpile-print-helper-dev.exe node e2e/run.mjs
//
// Steps: the owner makes a pairing code; the helper pairs with it; a label
// printer is kept by address (it answers ~HI as a Zebra); the helper runs;
// the owner picks the helper's printers; a test page goes to each; pausing
// at the helper makes a print fail at once in plain words; Remove in
// Stockpile stops the running helper.
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { createServer } from "node:net";
import { networkInterfaces, tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";

const STOCKPILE = process.env.STOCKPILE ?? "http://localhost:8071";
const ORIGIN = process.env.ORIGIN ?? "http://localhost:5191";
const HELPER = process.env.HELPER;
const PASSWORD = process.env.OWNER_PASSWORD;
assert.ok(HELPER && PASSWORD, "set HELPER and OWNER_PASSWORD");

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const step = (s) => console.log(`\n▶ ${s}`);
let cookie = "";

async function owner(path, init = {}) {
  const res = await fetch(`${STOCKPILE}/api${path}`, {
    ...init,
    headers: { Origin: ORIGIN, Cookie: cookie, ...(init.body ? { "Content-Type": "application/json" } : {}), ...(init.headers ?? {}) },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

// Async: the fake Zebra below lives in this process and must answer while the helper waits.
const helper = async (...args) => (await promisify(execFile)(HELPER, args, { encoding: "utf8" })).stdout;

async function until(what, fn, ms = 60_000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(500);
  }
}

// A private IPv4 address of this machine: the helper prints only to those.
const lanIp = Object.values(networkInterfaces())
  .flat()
  .find((a) => a && a.family === "IPv4" && !a.internal && /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(a.address))?.address;
assert.ok(lanIp, "this machine needs a private IPv4 address");

// The fake Zebra: answers ~HS (ready) and ~HI (its model), keeps the rest.
const received = [];
const zebra = createServer((sock) => {
  let buf = "";
  sock.on("data", (d) => {
    buf += d.toString("latin1");
    if (buf === "~HS") sock.end("\x02030,0,0,1218,000,0,0,0,000,0,0,0\x03\r\n\x02000,0,0,0,0,2,4,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n");
    else if (buf === "~HI") sock.end("\x02ZD620-203dpi,V84.20.18Z,8,8176KB\x03\r\n");
  });
  sock.on("end", () => {
    if (buf && buf !== "~HS" && buf !== "~HI") received.push(buf);
    sock.end();
  });
  sock.on("error", () => {});
});
await new Promise((r) => zebra.listen(9105, lanIp, r));
const zebraAt = `${lanIp}:9105`;

const config = mkdtempSync(join(tmpdir(), "ph-e2e-cfg-"));
const sink = mkdtempSync(join(tmpdir(), "ph-e2e-sink-"));
let run;
let helperId;

try {
  step("The owner signs in and makes a pairing code");
  const login = await fetch(`${STOCKPILE}/api/auth/password`, { method: "POST", headers: { Origin: ORIGIN, "Content-Type": "application/json" }, body: JSON.stringify({ password: PASSWORD }) });
  assert.equal(login.status, 201);
  cookie = login.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ");
  const code = await owner("/printing/helpers/pairing-codes", { method: "POST", body: "{}" });
  console.log(`  code ${code.code}`);

  step("The helper pairs with it; the same code then works no more");
  console.log("  " + (await helper("pair", "--config", config, "--server", STOCKPILE, code.code)).trim());
  const again = await (async () => {
    try {
      await helper("pair", "--config", mkdtempSync(join(tmpdir(), "ph-e2e-cfg2-")), "--server", STOCKPILE, code.code);
      return "paired again";
    } catch (e) {
      return String(e.stderr ?? e.message);
    }
  })();
  assert.match(again, /didn't work/);
  console.log("  a second pair with the same code is refused ✓");
  step("Keep the label printer by its address (it answers as a Zebra)");
  console.log("  " + (await helper("add-printer", zebraAt, "--config", config)).trim());
  console.log((await helper("printers", "--config", config)).trim().replace(/^/gm, "  "));

  step("Run the helper");
  run = spawn(HELPER, ["run", "--config", config, "--file-sink", sink], { stdio: ["ignore", "pipe", "pipe"] });
  run.stdout.on("data", (d) => process.stdout.write(`  [helper] ${d}`));
  run.stderr.on("data", (d) => process.stdout.write(`  [helper!] ${d}`));
  const mine = await until("the helper to report in", async () => {
    const list = await owner("/printing/helpers");
    return list.find((h) => h.state === "connected" && h.printers.some((p) => p.id.endsWith(`/raw:${zebraAt}`)));
  });
  helperId = mine.id;
  console.log(`  connected as ${mine.computer}, ${mine.printers.length} printers: ${mine.printers.map((p) => `${p.name} (${p.kind})`).join(", ")}`);

  step("The owner picks the helper's printers");
  await owner("/printing/printers/SHIPPING_LABEL", { method: "PUT", body: JSON.stringify({ pluginId: "helper", providerPrinterId: `${helperId}/raw:${zebraAt}`, format: "ZPL" }) });
  await owner("/printing/printers/DOCUMENT", { method: "PUT", body: JSON.stringify({ pluginId: "helper", providerPrinterId: `${helperId}/sink:page`, format: "PDF" }) });

  const settle = (id) => until(`job ${id} to settle`, async () => {
    const j = await owner(`/printing/jobs/${id}`);
    return ["PRINTED", "FAILED", "UNCERTAIN"].includes(j.status) || (j.status === "SENT" && j.expiresAt === null) ? j : null;
  });

  step("A test page to the label printer (raw ZPL over port 9105)");
  const label = await settle((await owner("/printing/printers/SHIPPING_LABEL/test-page", { method: "POST" })).id);
  console.log(`  ${label.status}${label.lastError ? ": " + label.lastError : ""}`);
  assert.equal(label.status, "SENT", "a raw printer can't confirm more than sent");
  await until("the fake Zebra to get it", () => received.length > 0, 10_000);
  assert.match(received[0], /^\^XA[\s\S]*Stockpile test page[\s\S]*\^XZ/);
  console.log(`  the fake Zebra got ${received[0].length} bytes of ZPL ✓`);

  step("A test page to the file printer (a PDF drawn as PNG pages)");
  const slip = await settle((await owner("/printing/printers/DOCUMENT/test-page", { method: "POST" })).id);
  assert.equal(slip.status, "PRINTED", slip.lastError ?? "");
  const pngs = readdirSync(sink).filter((f) => f.endsWith(".png"));
  assert.equal(pngs.length, 1);
  assert.equal(readFileSync(join(sink, pngs[0])).subarray(1, 4).toString(), "PNG");
  console.log(`  PRINTED; the file printer wrote ${pngs[0]} ✓`);

  step("Pause at the helper: a print fails at once, in words");
  await helper("pause", "--config", config);
  await until("Stockpile to see it paused", async () => (await owner("/printing/helpers")).find((h) => h.id === helperId)?.state === "paused", 40_000);
  const paused = await settle((await owner("/printing/printers/SHIPPING_LABEL/test-page", { method: "POST" })).id);
  assert.equal(paused.status, "FAILED");
  assert.match(paused.lastError, /is paused/);
  console.log(`  FAILED: ${paused.lastError} ✓`);
  await helper("resume", "--config", config);

  step("Remove in Stockpile stops the running helper");
  await owner(`/printing/helpers/${helperId}`, { method: "DELETE" });
  const exit = await Promise.race([new Promise((r) => run.on("exit", r)), sleep(15_000).then(() => "still running")]);
  assert.equal(exit, 3, `the helper should exit 3 (removed), got ${exit}`);
  console.log("  the helper stopped and forgot its token ✓");
  console.log("  " + (await helper("status", "--config", config)).trim());
  run = null;
  console.log("\nEnd-to-end: all passed.");
} finally {
  await cleanup();
}

async function cleanup() {
  if (run) run.kill();
  zebra.close();
  for (const role of ["SHIPPING_LABEL", "DOCUMENT"]) await owner(`/printing/printers/${role}`, { method: "DELETE" }).catch(() => {});
  if (helperId) await owner(`/printing/helpers/${helperId}`, { method: "DELETE" }).catch(() => {});
}
