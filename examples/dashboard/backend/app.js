// System Pulse's backend: JavaScript running in jsc2go, inside the Go
// program. The system comes in through Go modules (sys:*), the window
// through app:window: handle() answers the UI's invoke(), emit() sends it
// events.
import os from "sys:os";
import disk from "sys:disk";
import proc from "sys:proc";
import shell from "sys:shell";
import db from "sys:db";
import { handle, emit, setTitle } from "app:window";
import info from "app:info";

// ── Live CPU + memory, sampled every second and kept in SQLite ──────────────
db.exec("CREATE TABLE samples (t INTEGER PRIMARY KEY, cpu REAL, mem REAL)");

let previous = os.cpus().map(c => c.times);
function sample() {
  const now = os.cpus().map(c => c.times);
  const cores = now.map((t, i) => {
    const p = previous[i];
    const idle = t.idle - p.idle;
    const total = t.user + t.nice + t.sys + t.idle + t.irq - (p.user + p.nice + p.sys + p.idle + p.irq);
    return total > 0 ? Math.round((1 - idle / total) * 1000) / 10 : 0;
  });
  previous = now;
  const cpu = Math.round((cores.reduce((a, b) => a + b, 0) / cores.length) * 10) / 10;
  const m = os.memory();
  const memUsed = m.total - m.free;
  const mem = Math.round((memUsed / m.total) * 1000) / 10;
  db.exec("INSERT INTO samples VALUES (?, ?, ?)", Date.now(), cpu, mem);
  db.exec("DELETE FROM samples WHERE t < ?", Date.now() - 5 * 60_000);
  return { cpu, cores, mem, memUsed, memTotal: m.total, uptime: os.uptime(), appRss: proc.self().rss };
}

// ── One-off lookups ─────────────────────────────────────────────────────────
const lines = t => t.split(/\r?\n/).map(s => s.trim()).filter(Boolean);

let gpuCache;
function gpus() {
  gpuCache ??= (() => {
    try {
      switch (os.platform()) {
        case "windows":
          return lines(shell.run("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_VideoController).Name"));
        case "darwin": {
          const info = JSON.parse(shell.run("system_profiler", "SPDisplaysDataType", "-json"));
          return (info.SPDisplaysDataType ?? []).map(d => d.sppci_model ?? d._name).filter(Boolean);
        }
        default:
          return lines(shell.run("lspci"))
            .filter(l => /VGA|3D|Display/.test(l))
            .map(l => l.replace(/^\S+\s+[^:]+:\s*/, ""));
      }
    } catch {
      return [];
    }
  })();
  return gpuCache;
}

function systemInfo() {
  const cpus = os.cpus();
  const self = proc.self();
  return {
    hostname: os.hostname(),
    user: os.user(),
    os: os.version(),
    arch: os.arch(),
    cpu: cpus[0]?.model.trim(),
    cores: cpus.length,
    speedMHz: cpus[0]?.speed,
    memTotal: os.memory().total,
    gpus: gpus(),
    network: os.network() ?? [],
    engine: info.engine,
    go: info.go,
    pid: self.pid,
    exe: self.exe,
  };
}

function disks() {
  return disk.list() ?? [];
}

function processes() {
  const rows = proc.list();
  // Group by name: browsers and friends run as many processes.
  const byName = new Map();
  for (const r of rows) {
    const g = byName.get(r.name) ?? { name: r.name, count: 0, memKB: 0 };
    g.count++;
    g.memKB += r.memKB;
    byName.set(r.name, g);
  }
  return { total: rows.length, top: [...byName.values()].sort((a, b) => b.memKB - a.memKB).slice(0, 10) };
}

function history() {
  // SQL over the last minute.
  const since = Date.now() - 60_000;
  return {
    points: db.query("SELECT cpu, mem FROM samples WHERE t >= ? ORDER BY t", since) ?? [],
    stats: db.get("SELECT ROUND(AVG(cpu), 1) AS avgCpu, MAX(cpu) AS maxCpu, ROUND(AVG(mem), 1) AS avgMem, COUNT(*) AS n FROM samples WHERE t >= ?", since),
  };
}

// ── jsc2go speed test: JavaScript, compiled by jsc2go's JIT ─────────────────
function sha256(bytes) {
  const K = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ];
  const H = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
  const n = bytes.length, total = ((n + 9 + 63) >> 6) << 6;
  const msg = new Uint8Array(total);
  msg.set(bytes);
  msg[n] = 0x80;
  const bits = n * 8;
  msg[total - 4] = bits >>> 24; msg[total - 3] = bits >>> 16; msg[total - 2] = bits >>> 8; msg[total - 1] = bits;
  const w = new Int32Array(64);
  for (let off = 0; off < total; off += 64) {
    for (let i = 0; i < 16; i++) {
      const j = off + i * 4;
      w[i] = (msg[j] << 24) | (msg[j + 1] << 16) | (msg[j + 2] << 8) | msg[j + 3];
    }
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15], b = w[i - 2];
      const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3);
      const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0;
    }
    let [a, b, c, d, e, f, g, h] = H;
    for (let i = 0; i < 64; i++) {
      const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      const t1 = (h + S1 + ((e & f) ^ (~e & g)) + K[i] + w[i]) | 0;
      const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      const t2 = (S0 + ((a & b) ^ (a & c) ^ (b & c))) | 0;
      h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    H[0] = (H[0] + a) | 0; H[1] = (H[1] + b) | 0; H[2] = (H[2] + c) | 0; H[3] = (H[3] + d) | 0;
    H[4] = (H[4] + e) | 0; H[5] = (H[5] + f) | 0; H[6] = (H[6] + g) | 0; H[7] = (H[7] + h) | 0;
  }
  return H.map(x => (x >>> 0).toString(16).padStart(8, "0")).join("");
}

function benchmark() {
  const time = fn => {
    const t = Date.now();
    const result = fn();
    return { ms: Math.max(1, Date.now() - t), result };
  };
  const data = new Uint8Array(4 * 1024 * 1024);
  for (let i = 0; i < data.length; i++) data[i] = (i * 2654435761) >>> 24;
  const hash = time(() => sha256(data));
  const fib = n => (n < 2 ? n : fib(n - 1) + fib(n - 2));
  const calls = time(() => fib(30));
  const objects = time(() => {
    let sum = 0;
    for (let i = 0; i < 2_000_000; i++) { const p = { x: i, y: i * 2 }; sum += p.x + p.y; }
    return sum;
  });
  const json = time(() => {
    const doc = Array.from({ length: 20_000 }, (_, i) => ({ id: i, name: "item " + i, tags: ["a", "b"], price: i / 3 }));
    return JSON.parse(JSON.stringify(doc)).length;
  });
  return {
    sha256: { ms: hash.ms, mbps: Math.round((data.length / 1048576) / (hash.ms / 1000)), digest: hash.result.slice(0, 16) },
    fib: { ms: calls.ms, calls: 1664079, perSec: Math.round(1664079 / (calls.ms / 1000)) },
    objects: { ms: objects.ms, perSec: Math.round(2_000_000 / (objects.ms / 1000)) },
    json: { ms: json.ms, items: json.result },
  };
}

// ── Window ──────────────────────────────────────────────────────────────────
// (Edit this file while the app runs: it reloads, with the window, without a
// rebuild. setSize(w, h) and center() are there too.)
setTitle(`System Pulse: ${os.hostname()}`);
handle("system", systemInfo);
handle("disks", disks);
handle("processes", processes);
handle("history", history);
handle("benchmark", benchmark);

setInterval(() => emit("stats", sample()), 1000);
