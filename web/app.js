// Argus dashboard — zero dependencies. Live over SSE, canvas charts, incremental DOM.
"use strict";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
// DOM helpers ignore null/false children (conditional UI) instead of printing "null".
for (const m of ["replaceChildren", "append"]) {
  const orig = Element.prototype[m];
  Element.prototype[m] = function (...k) { return orig.apply(this, k.flat(9).filter((x) => x != null && x !== false)); };
}
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "html") el.innerHTML = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat(9)) if (c != null && c !== false) el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  return el;
}

// ------------------------------------------------------------------ icons (inline SVG, stroke-based)
const P = {
  cpu: '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
  mem: '<rect x="2" y="6" width="20" height="10" rx="2"/><path d="M6 10v2M10 10v2M14 10v2M18 10v2M5 16v3M9 16v3M15 16v3M19 16v3"/>',
  gpu: '<rect x="2" y="6" width="20" height="12" rx="2"/><circle cx="9" cy="12" r="3"/><path d="M15 10h3M15 14h3M6 18v2M18 18v2"/>',
  net: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  down: '<path d="M12 5v14M19 12l-7 7-7-7"/>',
  up: '<path d="M12 19V5M5 12l7-7 7 7"/>',
  disk: '<path d="M22 12H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><path d="M6 16h.01M10 16h.01"/>',
  temp: '<path d="M14 14.76V3.5a2.5 2.5 0 0 0-5 0v11.26a4.5 4.5 0 1 0 5 0z"/>',
  fan: '<circle cx="12" cy="12" r="2"/><path d="M12 10c0-4 1-7 4-7s3 4-2 7M14 12c4 0 7 1 7 4s-4 3-7-2M12 14c0 4-1 7-4 7s-3-4 2-7M10 12c-4 0-7-1-7-4s4-3 7 2"/>',
  zap: '<path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/>',
  battery: '<rect x="2" y="7" width="16" height="10" rx="2"/><path d="M22 11v2M6 11v2"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  server: '<rect x="3" y="3" width="18" height="8" rx="2"/><rect x="3" y="13" width="18" height="8" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
  term: '<path d="m4 17 6-6-6-6M12 19h8"/>',
  box: '<path d="M21 8 12 3 3 8v8l9 5 9-5V8z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  layers: '<path d="m12 2 10 5-10 5L2 7l10-5z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>',
  play: '<path d="M7 4.5v15l12-7.5-12-7.5z"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="1.5"/>',
  restart: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
  pause: '<path d="M8 5v14M16 5v14"/>',
  logs: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01"/>',
  power: '<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.8 0"/>',
  reboot: '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><path d="m9 12 2 2 4-4"/>',
  lock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  back: '<path d="m15 18-6-6 6-6"/>',
  alert: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
  heart: '<path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1-1.1a5.5 5.5 0 0 0-7.8 7.8L12 21l8.8-8.6a5.5 5.5 0 0 0 0-7.8z"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>',
  plug: '<path d="M12 22v-5M9 8V2M15 8V2M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8z"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>',
  monitor: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>',
  usb: '<path d="M12 2v14M9 5l3-3 3 3M8 9v2l4 3 4-3V8"/><circle cx="12" cy="19" r="3"/>',
  puzzle: '<path d="M19.4 14.6a2 2 0 0 0-1.4-.6H17v-3h1a2 2 0 1 0 0-4h-1V4h-4v1a2 2 0 1 1-4 0V4H5v4h1a2 2 0 1 1 0 4H5v4h4v-1a2 2 0 1 1 4 0v1h4v-1h1a2 2 0 0 0 1.4-3.4z"/>',
  hash: '<path d="M4 9h16M4 15h16M10 3 8 21M16 3l-2 18"/>',
  activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  overview: '<path d="M3 3v18h18"/><path d="m7 15 4-4 3 3 5-6"/>',
  wave: '<path d="M2 12c2 0 2-6 4-6s2 12 4 12 2-12 4-12 2 12 4 12 2-6 4-6"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m10.8 12.2 9.2-9.2M16 7l3 3M18 5l2 2"/>',
  docker: '<path d="M2 13h17.5c.9 0 1.8-.9 2.2-2 .5.1 1.1 0 1.3-.3-.3-.6-1-.8-1.5-.7-.2-.9-.8-1.6-1.4-2-.4.9-.4 1.9-.1 2.6M2 13c0 4 3 7 8 7 5.5 0 8.6-2.8 9.8-7"/><path d="M5 10h3v3M8 10h3v3M11 10h3v3M8 7h3v3M11 7h3v3"/>',
  inspect: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3M11 8v6M8 11h6"/>',
};
const ic = (n, cls = "") => {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24"); s.setAttribute("fill", "none"); s.setAttribute("stroke", "currentColor");
  s.setAttribute("stroke-width", "1.8"); s.setAttribute("stroke-linecap", "round"); s.setAttribute("stroke-linejoin", "round");
  s.setAttribute("class", "ic " + cls); s.setAttribute("aria-hidden", "true"); s.innerHTML = P[n] || P.info;
  return s;
};
const OS = {
  darwin: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M16.4 12.6c0-2.3 1.9-3.4 2-3.5-1.1-1.6-2.8-1.8-3.4-1.8-1.4-.1-2.8.9-3.5.9s-1.8-.9-3-.8C7 7.4 5.6 8.3 4.8 9.7c-1.7 2.9-.4 7.2 1.2 9.6.8 1.2 1.7 2.4 3 2.4 1.2 0 1.6-.8 3.1-.8s1.8.8 3.1.8c1.3 0 2.1-1.2 2.9-2.3.9-1.3 1.3-2.6 1.3-2.7 0 0-2.5-1-2.5-3.9ZM14.1 5.8c.6-.8 1.1-1.9 1-3-.9 0-2.1.6-2.7 1.4-.6.7-1.1 1.8-1 2.9 1 .1 2.1-.5 2.7-1.3Z"/></svg>',
  windows: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M3 5.5 10 4.5v7H3zM11 4.3 21 3v8.5H11zM3 12.5h7v7l-7-1zM11 12.5h10V21l-10-1.4z"/></svg>',
  linux: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2c-2.2 0-3.6 1.9-3.6 4.4 0 1.5.4 2.4-.4 3.8C6.6 12.5 5 14.4 5 17c0 .8.2 1.5.6 2.1-.6.3-1.6.6-1.6 1.4 0 1 1.6 1.5 3.5 1.5 1.4 0 2.4-.4 3-.9.5.1 1.5.1 3 0 .6.5 1.6.9 3 .9 1.9 0 3.5-.5 3.5-1.5 0-.8-1-1.1-1.6-1.4.4-.6.6-1.3.6-2.1 0-2.6-1.6-4.5-3-6.8-.8-1.4-.4-2.3-.4-3.8C15.6 3.9 14.2 2 12 2Zm-1.6 4.2c.4 0 .7.4.7 1s-.3 1-.7 1-.7-.4-.7-1 .3-1 .7-1Zm3.2 0c.4 0 .7.4.7 1s-.3 1-.7 1-.7-.4-.7-1 .3-1 .7-1ZM12 9.2c.9 0 1.8.5 1.8.9s-.9 1-1.8 1-1.8-.6-1.8-1 .9-.9 1.8-.9Z"/></svg>',
};
const osIcon = (os) => h("div", { class: "osicon", html: OS[os] || "" }, OS[os] ? null : ic("server"));

// ------------------------------------------------------------------ format
const fmt = {
  bytes(b, d = 1) {
    if (b == null || isNaN(b)) return "–";
    const u = ["B", "KB", "MB", "GB", "TB", "PB"]; let i = 0;
    while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
    return (i === 0 ? Math.round(b) : b.toFixed(b >= 100 ? 0 : d)) + " " + u[i];
  },
  bits(Bps) {
    let b = (Bps || 0) * 8; const u = ["b/s", "Kb/s", "Mb/s", "Gb/s", "Tb/s"]; let i = 0;
    while (b >= 1000 && i < u.length - 1) { b /= 1000; i++; }
    return (i === 0 ? Math.round(b) : b.toFixed(b >= 100 ? 0 : 1)) + " " + u[i];
  },
  rate(Bps) { return fmt.bytes(Bps || 0) + "/s"; },
  pct(v) { return v == null || isNaN(v) ? "–" : (v >= 10 ? Math.round(v) : v.toFixed(1)); },
  dur(s) {
    if (!s) return "–";
    const d = Math.floor(s / 86400), hh = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    return d ? `${d}d ${hh}h` : hh ? `${hh}h ${m}m` : `${m}m`;
  },
  ago(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 5) return "just now"; if (s < 60) return s + "s ago"; if (s < 3600) return Math.round(s / 60) + "m ago";
    if (s < 86400) return Math.round(s / 3600) + "h ago"; return Math.round(s / 86400) + "d ago";
  },
  mhz(v) { return !v ? "–" : v >= 1000 ? (v / 1000).toFixed(2) + " GHz" : Math.round(v) + " MHz"; },
  time(t) { return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }); },
};
const split = (s) => { const i = s.indexOf(" "); return i < 0 ? [s, ""] : [s.slice(0, i), s.slice(i + 1)]; };
const valUnit = (s) => { const [v, u] = split(s); return [v, h("small", null, u)]; };

// ------------------------------------------------------------------ state
const S = {
  nodes: new Map(), skew: 0, view: lsGet("argus.view") || "grid", q: "",
  cards: new Map(), detail: null, pending: new Map(), info: null, es: null, focus: "", live: false, hoverT: null,
};
function lsGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
function lsSet(k, v) { try { localStorage.setItem(k, v); } catch {} }
const now = () => Date.now() + S.skew;
function status(n) {
  const age = now() - (n.seen || 0), iv = Math.max(n.iv || 2000, 2000);
  return age < iv * 2.5 + 4000 ? "on" : age < 90000 ? "stale" : "off";
}
const HIST_MAX = 1800;

// ------------------------------------------------------------------ API
async function api(path, opts = {}) {
  const r = await fetch(path, { credentials: "same-origin", ...opts, headers: { "Content-Type": "application/json", "X-Argus": "1", ...(opts.headers || {}) } });
  if (r.status === 401) { showLogin(); throw new Error("unauthorized"); }
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r.status === 204 ? null : r.json();
}
function toast(msg, err) {
  const t = h("div", { class: "toast" + (err ? " err" : "") }, ic(err ? "alert" : "check"), h("div", null, msg));
  $("#toasts").append(t);
  setTimeout(() => t.remove(), 5000);
}

// ------------------------------------------------------------------ charts
const cssCache = new Map();
const css = (v) => { if (!cssCache.has(v)) cssCache.set(v, getComputedStyle(document.documentElement).getPropertyValue(v).trim()); return cssCache.get(v); };
matchMedia("(prefers-color-scheme: light)").addEventListener?.("change", () => { cssCache.clear(); redrawAll(); });
let tipEl;
function tip(x, y, html) {
  if (!tipEl) { tipEl = h("div", { class: "tip" }); document.body.append(tipEl); }
  if (html == null) { tipEl.style.display = "none"; return; }
  tipEl.innerHTML = html; tipEl.style.display = "block";
  const r = tipEl.getBoundingClientRect();
  let left = x + 16; if (left + r.width > innerWidth - 8) left = x - r.width - 16;
  tipEl.style.left = Math.max(8, left) + "px";
  tipEl.style.top = Math.max(8, Math.min(y - r.height / 2, innerHeight - r.height - 8)) + "px";
}
const charts = new Set();
function redrawAll() { for (const c of charts) c.draw(); }

class Chart {
  // series: [{i, color, label}] ; opts: max, fmtY, spark, legend element
  constructor(canvas, { series, max, fmtY = (v) => v, spark = false, legend }) {
    Object.assign(this, { c: canvas, series, max, fmtY, spark, legend, data: [], hidden: new Set() });
    this.ro = new ResizeObserver(() => this.resize()); this.ro.observe(canvas);
    charts.add(this);
    if (!spark) {
      const move = (e) => this.onMove(e.touches ? e.touches[0] : e);
      canvas.addEventListener("mousemove", move);
      canvas.addEventListener("touchmove", move, { passive: true });
      const leave = () => { S.hoverT = null; tip(0, 0, null); redrawAll(); };
      canvas.addEventListener("mouseleave", leave); canvas.addEventListener("touchend", leave);
    }
    if (legend) this.buildLegend();
  }
  destroy() { this.ro.disconnect(); charts.delete(this); }
  buildLegend() {
    this.legendVals = this.series.map((s) => h("b"));
    this.legend.replaceChildren(...this.series.map((s, k) => h("button", { onclick: (e) => {
      this.hidden.has(k) ? this.hidden.delete(k) : this.hidden.add(k);
      e.currentTarget.classList.toggle("off"); this.draw();
    } }, h("i", { style: { background: `var(${s.color})` } }), s.label, this.legendVals[k])));
  }
  resize() {
    const dpr = Math.min(devicePixelRatio || 1, 2), r = this.c.getBoundingClientRect();
    if (!r.width) return;
    this.c.width = Math.round(r.width * dpr); this.c.height = Math.round(r.height * dpr); this.dpr = dpr; this.draw();
  }
  set(data) {
    this.data = data;
    if (this.legendVals && data.length) {
      const last = data[data.length - 1];
      this.series.forEach((s, k) => (this.legendVals[k].textContent = this.fmtY(last[s.i] || 0)));
    }
    this.draw();
  }
  pad() { return this.spark ? { l: 0, r: 0, t: 4, b: 2 } : { l: 52, r: 10, t: 12, b: 24 }; }
  onMove(e) {
    const r = this.c.getBoundingClientRect(), d = this.data; if (d.length < 2) return;
    const pad = this.pad(), w = r.width - pad.l - pad.r;
    const t0 = d[0][0], t1 = d[d.length - 1][0];
    S.hoverT = t0 + Math.max(0, Math.min(1, (e.clientX - r.left - pad.l) / w)) * (t1 - t0);
    redrawAll();
    const p = this.nearest(S.hoverT);
    tip(e.clientX, e.clientY, `<div class="faint">${new Date(p[0] * 1000).toLocaleTimeString()}</div>` +
      this.series.filter((_, k) => !this.hidden.has(k)).map((s) => `<div><i style="background:${css(s.color)}"></i>${s.label} <b>${this.fmtY(p[s.i] || 0)}</b></div>`).join(""));
  }
  nearest(t) {
    const d = this.data; let lo = 0, hi = d.length - 1;
    while (hi - lo > 1) { const m = (lo + hi) >> 1; d[m][0] < t ? (lo = m) : (hi = m); }
    return Math.abs(d[lo][0] - t) < Math.abs(d[hi][0] - t) ? d[lo] : d[hi];
  }
  draw() {
    const c = this.c, x = c.getContext("2d"), dpr = this.dpr || 1, d = this.data;
    if (!c.width) return;
    const W = c.width / dpr, H = c.height / dpr;
    x.setTransform(dpr, 0, 0, dpr, 0, 0); x.clearRect(0, 0, W, H);
    const pad = this.pad(), w = W - pad.l - pad.r, hh = H - pad.t - pad.b;
    const vis = this.series.filter((_, k) => !this.hidden.has(k));
    let max = this.max;
    if (max == null) { max = 0; for (const p of d) for (const s of vis) max = Math.max(max, p[s.i] || 0); max = niceMax(max || 1); }
    if (!this.spark) {
      x.font = "10.5px " + css("--mono"); x.fillStyle = css("--faint"); x.strokeStyle = css("--line"); x.lineWidth = 1;
      x.setLineDash([3, 4]);
      for (let g = 0; g <= 4; g++) {
        const y = Math.round(pad.t + hh - (g / 4) * hh) + .5;
        x.beginPath(); x.moveTo(pad.l, y); x.lineTo(W - pad.r, y); x.stroke();
        x.textAlign = "right"; x.textBaseline = "middle"; x.fillText(this.fmtY((max * g) / 4), pad.l - 8, y);
      }
      x.setLineDash([]);
      if (d.length > 1) {
        const t0 = d[0][0], t1 = d[d.length - 1][0], nt = Math.max(1, Math.min(5, Math.floor(w / 110)));
        x.textBaseline = "alphabetic";
        for (let g = 0; g <= nt; g++) {
          const t = t0 + ((t1 - t0) * g) / nt;
          x.textAlign = g === 0 ? "left" : g === nt ? "right" : "center";
          x.fillText(new Date(t * 1000).toLocaleTimeString([], t1 - t0 > 7200 ? { hour: "2-digit", minute: "2-digit" } : { hour: "2-digit", minute: "2-digit", second: t1 - t0 < 900 ? "2-digit" : undefined }), pad.l + (w * g) / nt, H - 6);
        }
      }
    }
    if (d.length < 2) {
      if (!this.spark) { x.fillStyle = css("--faint"); x.textAlign = "center"; x.fillText("collecting…", pad.l + w / 2, pad.t + hh / 2); }
      return;
    }
    const t0 = d[0][0], t1 = d[d.length - 1][0], span = t1 - t0 || 1;
    const X = (t) => pad.l + ((t - t0) / span) * w, Y = (v) => pad.t + hh - Math.min(1, Math.max(0, (v || 0) / max)) * hh;
    for (const s of vis) {
      const col = css(s.color);
      const pts = d.map((p) => [X(p[0]), Y(p[s.i])]);
      x.beginPath(); smooth(x, pts);
      x.strokeStyle = col; x.lineWidth = this.spark ? 1.5 : 1.8; x.lineJoin = "round"; x.stroke();
      x.lineTo(pts[pts.length - 1][0], pad.t + hh); x.lineTo(pts[0][0], pad.t + hh); x.closePath();
      const g = x.createLinearGradient(0, pad.t, 0, pad.t + hh);
      g.addColorStop(0, col + (vis.length > 1 ? "2a" : "40")); g.addColorStop(1, col + "00");
      x.fillStyle = g; x.fill();
    }
    if (!this.spark && S.hoverT != null && S.hoverT >= t0 && S.hoverT <= t1) {
      const p = this.nearest(S.hoverT), px = X(p[0]);
      x.strokeStyle = css("--line2"); x.lineWidth = 1; x.beginPath(); x.moveTo(px, pad.t); x.lineTo(px, pad.t + hh); x.stroke();
      for (const s of vis) {
        x.fillStyle = css("--panel-solid"); x.beginPath(); x.arc(px, Y(p[s.i]), 4.5, 0, 7); x.fill();
        x.fillStyle = css(s.color); x.beginPath(); x.arc(px, Y(p[s.i]), 3, 0, 7); x.fill();
      }
    }
  }
}
// Monotone cubic interpolation (no overshoot — values never dip below 0 or exceed peaks).
function smooth(x, p) {
  const n = p.length; x.moveTo(p[0][0], p[0][1]);
  if (n < 3) { for (let i = 1; i < n; i++) x.lineTo(p[i][0], p[i][1]); return; }
  const m = new Array(n), dx = [], dy = [], s = [];
  for (let i = 0; i < n - 1; i++) { dx[i] = p[i + 1][0] - p[i][0] || 1e-6; dy[i] = p[i + 1][1] - p[i][1]; s[i] = dy[i] / dx[i]; }
  m[0] = s[0]; m[n - 1] = s[n - 2];
  for (let i = 1; i < n - 1; i++) m[i] = s[i - 1] * s[i] <= 0 ? 0 : (3 * (dx[i - 1] + dx[i])) / ((2 * dx[i] + dx[i - 1]) / s[i - 1] + (dx[i] + 2 * dx[i - 1]) / s[i]);
  for (let i = 0; i < n - 1; i++) {
    const h3 = dx[i] / 3;
    x.bezierCurveTo(p[i][0] + h3, p[i][1] + m[i] * h3, p[i + 1][0] - h3, p[i + 1][1] - m[i + 1] * h3, p[i + 1][0], p[i + 1][1]);
  }
}
function niceMax(v) {
  const e = Math.pow(10, Math.floor(Math.log10(v))), f = v / e;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * e;
}
function ring(color, icon) {
  const NS = "http://www.w3.org/2000/svg", s = document.createElementNS(NS, "svg");
  s.setAttribute("viewBox", "0 0 52 52"); s.setAttribute("class", "ring");
  s.innerHTML = `<circle class="track" cx="26" cy="26" r="21"/><circle class="val" cx="26" cy="26" r="21" stroke="var(${color})" stroke-dasharray="131.95" stroke-dashoffset="131.95"/>`;
  const wrap = h("div", { class: "ring-ic", style: { color: `var(${color})` } }, s, ic(icon));
  wrap.set = (pct) => { s.lastChild.style.strokeDashoffset = 131.95 * (1 - Math.min(100, Math.max(0, pct || 0)) / 100); };
  return wrap;
}

// ------------------------------------------------------------------ derived
const memPct = (m) => (m && m.mem && m.mem.total ? (m.mem.used / m.mem.total) * 100 : 0);
const gpuPct = (m) => (m && m.gpus && m.gpus.length ? m.gpus.reduce((a, g) => a + g.util, 0) / m.gpus.length : null);
function netSum(n) {
  const m = n.last; if (!m || !m.net) return [0, 0];
  const virt = new Set((n.static?.nics || []).filter((x) => x.virt).map((x) => x.name));
  let rx = 0, tx = 0; for (const i of m.net) if (!virt.has(i.name)) { rx += i.rx; tx += i.tx; }
  return [rx, tx];
}
function maxTemp(m) {
  let t = 0; for (const s of m?.temps || []) if (s.v < 150 && !/power/i.test(s.name)) t = Math.max(t, s.v);
  for (const g of m?.gpus || []) t = Math.max(t, g.temp || 0); return t || null;
}
const title = (n) => n.static?.hostname || n.id.slice(0, 8);
const subtitle = (st) => (st ? [st.platform, st.model].filter(Boolean).join(" · ") : "");
const barColor = (v) => (v > 90 ? "var(--bad)" : v > 75 ? "var(--warn)" : null);
const ctrCount = (m) => { const c = m?.docker?.containers || []; return [c.filter((x) => x.state === "running").length, c.length]; };

// ------------------------------------------------------------------ fleet view
function renderSummary() {
  const nodes = [...S.nodes.values()], on = nodes.filter((n) => status(n) === "on");
  let threads = 0, cpuW = 0, mem = 0, memUsed = 0, rx = 0, tx = 0, gpus = 0, run = 0, ctrs = 0, selfCpu = 0, selfRss = 0;
  for (const n of on) {
    const t = n.static?.cpuThreads || 0; threads += t; cpuW += (n.last?.cpu || 0) * t;
    mem += n.last?.mem?.total || 0; memUsed += n.last?.mem?.used || 0;
    const [a, b] = netSum(n); rx += a; tx += b; gpus += n.static?.gpus?.length || 0;
    const [r, c] = ctrCount(n.last); run += r; ctrs += c;
    selfCpu += n.last?.self?.cpu || 0; selfRss += n.last?.self?.rss || 0;
  }
  const pill = (icon, ...kids) => h("span", { class: "pill" }, ic(icon), ...kids);
  $("#summary").replaceChildren(
    pill("server", h("b", null, `${on.length}/${nodes.length}`), "online"),
    pill("cpu", h("b", null, threads), "threads", h("b", null, fmt.pct(threads ? cpuW / threads : 0) + "%")),
    pill("mem", h("b", null, fmt.bytes(memUsed, 0)), "of", fmt.bytes(mem, 0)),
    gpus ? pill("gpu", h("b", null, gpus), "GPUs") : null,
    ctrs ? pill("box", h("b", null, `${run}/${ctrs}`), "containers") : null,
    pill("net", h("b", { style: { color: "var(--rx)" } }, "↓ " + fmt.bits(rx)), h("b", { style: { color: "var(--tx)" } }, "↑ " + fmt.bits(tx))),
    on.length ? h("span", { class: "pill", title: "Total CPU and memory used by Argus agents across the fleet" }, ic("eye"), "Argus", h("b", null, fmt.pct(selfCpu) + "%"), h("b", null, fmt.bytes(selfRss, 0))) : null,
  );
}

function visibleNodes() {
  const q = S.q.toLowerCase();
  return [...S.nodes.values()].filter((n) => {
    if (!q) return true;
    const st = n.static || {};
    return [st.hostname, st.platform, st.model, st.cpuModel, st.arch, st.os, n.addr, ...(st.gpus || []).map((g) => g.name), ...(n.last?.docker?.containers || []).map((c) => c.name)].join(" ").toLowerCase().includes(q);
  }).sort((a, b) => (status(a) === "off") - (status(b) === "off") || title(a).localeCompare(title(b)));
}

function renderFleet() {
  closeDetail();
  const main = $("#main"), nodes = visibleNodes();
  if (!S.nodes.size) {
    main.replaceChildren(h("div", { class: "hero-empty" },
      h("div", { class: "osicon", style: { width: "64px", height: "64px", margin: "0 auto", color: "var(--cpu)" } }, ic("eye", "lg")),
      h("h2", null, "Watch your first machine"),
      h("p", { class: "muted" }, "Run one line on any Mac, Windows PC, Linux box or VPS. It shows up here within seconds, over an encrypted, pinned connection."),
      h("button", { class: "btn primary", onclick: showAdd }, ic("plus"), "Add a machine")));
    return;
  }
  if (S.view === "list") return renderList(nodes);
  const grid = h("div", { class: "grid" });
  for (const n of nodes) grid.append(buildCard(n));
  main.replaceChildren(grid);
}

function buildCard(n) {
  const st = n.static || {}, r = {};
  const metric = (key, label, icon) => h("div", { class: "m4" }, h("label", null, ic(icon), label),
    (r[key] = h("div", { class: "v" }, "–")), h("div", { class: "bar" }, (r[key + "Bar"] = h("i", { style: { width: "0%", background: `var(--${key})` } }))));
  const card = h("div", { class: "card", onclick: () => (location.hash = "#/n/" + n.id) },
    h("div", { class: "card-head" }, osIcon(st.os),
      h("div", { class: "card-title" }, h("h3", null, (r.dot = h("span", { class: "dot" })), (r.title = h("span", null, title(n)))), (r.sub = h("div", { class: "sub" }))),
      h("div", { class: "card-meta" }, h("span", { class: "tag" }, ic("cpu"), st.arch || "?"), (r.up = h("span", { class: "faint", style: { fontSize: "11.5px" } })))),
    h("div", { class: "metrics4" }, metric("cpu", "CPU", "cpu"), metric("mem", "Memory", "mem"), metric("gpu", "GPU", "gpu"),
      h("div", { class: "m4" }, h("label", null, ic("net"), "Network"), (r.net = h("div", { class: "net2" }, "–")))),
    (r.spark = h("canvas", { class: "spark" })),
    (r.foot = h("div", { class: "card-foot" })));
  r.chart = new Chart(r.spark, { spark: true, max: 100, series: [{ i: 1, color: "--cpu" }, { i: 2, color: "--mem" }, { i: 3, color: "--gpu" }] });
  S.cards.set(n.id, { el: card, r });
  updateCard(n);
  return card;
}

function updateCard(n) {
  const c = S.cards.get(n.id); if (!c) return;
  const { r, el } = c, m = n.last, st = n.static || {}, s = status(n);
  el.classList.toggle("offline", s === "off");
  r.dot.className = "dot " + (s === "on" ? "" : s);
  r.title.textContent = title(n); r.sub.textContent = subtitle(st);
  r.up.textContent = s === "off" ? "seen " + fmt.ago(now() - n.seen) : "up " + fmt.dur(m?.uptime);
  const set = (k, v) => {
    r[k].replaceChildren(v == null ? "–" : fmt.pct(v), h("small", null, v == null ? "" : "%"));
    r[k + "Bar"].style.width = (v || 0) + "%"; r[k + "Bar"].style.background = barColor(v) || `var(--${k})`;
  };
  set("cpu", m?.cpu); set("mem", m ? memPct(m) : null); set("gpu", gpuPct(m));
  const [rx, tx] = netSum(n);
  r.net.replaceChildren(h("div", { class: "rx" }, "↓ " + fmt.bits(rx)), h("div", { class: "tx" }, "↑ " + fmt.bits(tx)));
  r.chart.set((n.hist || []).slice(-150));
  const chips = [];
  if (st.cpuThreads) chips.push(["cpu", `${st.pCores ? st.pCores + "P+" + st.eCores + "E" : st.cpuCores + "c"} / ${st.cpuThreads}t`]);
  if (st.memTotal) chips.push(["mem", fmt.bytes(st.memTotal, 0)]);
  for (const g of st.gpus || []) chips.push(["gpu", g.name.replace(/NVIDIA |GeForce |AMD |Radeon\(TM\) |\(TM\)/g, "") + (g.cores ? ` · ${g.cores}c` : ""), "accent"]);
  const [run, tot] = ctrCount(m); if (tot) chips.push(["docker", `${run}/${tot}`, "cyan"]);
  const t = maxTemp(m); if (t) chips.push(["temp", Math.round(t) + "°C", t > 85 ? "bad" : ""]);
  if (m?.sysW) chips.push(["zap", m.sysW + " W"]);
  if (m?.battery) chips.push(["battery", Math.round(m.battery.pct) + "%" + (m.battery.charging ? " ⚡" : "")]);
  if (st.virt) chips.push(["monitor", st.virt, "warn"]);
  if (st.manage) chips.push(["sliders", "managed"]);
  const key = JSON.stringify(chips);
  if (r.foot.dataset.k !== key) {
    r.foot.dataset.k = key;
    r.foot.replaceChildren(...chips.map(([i, txt, cls]) => h("span", { class: "tag " + (cls || "") }, ic(i), txt)));
  }
}

function renderList(nodes) {
  const mini = (v, col) => h("span", null, h("span", { class: "mini-bar" }, h("i", { style: { width: (v || 0) + "%", background: barColor(v) || col } })), h("span", { class: "num" }, v == null ? "–" : fmt.pct(v) + "%"));
  const rows = nodes.map((n) => {
    const m = n.last, st = n.static || {}, s = status(n), [rx, tx] = netSum(n), [run, tot] = ctrCount(m);
    return h("tr", { class: "click", onclick: () => (location.hash = "#/n/" + n.id) },
      h("td", null, h("span", { style: { display: "inline-flex", gap: "10px", alignItems: "center" } }, h("span", { class: "dot " + (s === "on" ? "" : s) }), h("b", null, title(n)))),
      h("td", { class: "muted hide-sm" }, st.platform || ""), h("td", { class: "hide-sm" }, h("span", { class: "tag" }, st.arch || "")),
      h("td", null, mini(m?.cpu, "var(--cpu)")), h("td", null, mini(m ? memPct(m) : null, "var(--mem)")), h("td", { class: "hide-sm" }, mini(gpuPct(m), "var(--gpu)")),
      h("td", { class: "num", style: { color: "var(--rx)" } }, fmt.bits(rx)), h("td", { class: "num", style: { color: "var(--tx)" } }, fmt.bits(tx)),
      h("td", { class: "num hide-sm" }, tot ? `${run}/${tot}` : "–"),
      h("td", { class: "num hide-sm" }, maxTemp(m) ? Math.round(maxTemp(m)) + "°" : "–"),
      h("td", { class: "num muted" }, s === "off" ? fmt.ago(now() - n.seen) : fmt.dur(m?.uptime)), h("td", { class: "mono muted hide-sm" }, n.addr || ""));
  });
  const cols = [["Machine"], ["Platform", 1], ["Arch", 1], ["CPU"], ["Memory"], ["GPU", 1], ["Rx"], ["Tx"], ["Containers", 1], ["Temp", 1], ["Uptime"], ["Address", 1]];
  $("#main").replaceChildren(h("div", { class: "list" }, h("table", null,
    h("thead", null, h("tr", null, cols.map(([t, hide]) => h("th", { class: hide ? "hide-sm" : "" }, t)))), h("tbody", null, rows))));
}

// ------------------------------------------------------------------ detail view
const TABS = [["overview", "Overview", "overview"], ["containers", "Containers", "docker"], ["processes", "Processes", "activity"], ["hardware", "Hardware", "cpu"], ["devices", "Devices", "usb"], ["network", "Network", "net"], ["console", "Console", "term"]];

function closeDetail() {
  if (S.detail) for (const c of Object.values(S.detail.charts)) c.destroy?.();
  for (const { r } of S.cards.values()) r.chart.destroy();
  S.detail = null; S.cards.clear();
}

async function renderDetail(id, tab = "overview") {
  let n = S.nodes.get(id);
  if (!n) { $("#main").replaceChildren(h("div", { class: "empty" }, "Machine not found. ", h("a", { href: "#/" }, "Back to fleet"))); return; }
  const prev = S.detail && S.detail.id === id ? S.detail : null;
  closeDetail();
  S.detail = { id, tab, range: prev?.range || "5m", charts: {}, refs: {}, procSort: prev?.procSort || "cpu", ctrFilter: prev?.ctrFilter || "all" };
  setFocus(id);
  if (!prev || !n.full) {
    try {
      const full = await api("/api/node/" + id);
      n = S.nodes.get(id) || n;
      n.hist = full.hist || n.hist || []; n.slow = full.slow || []; n.results = full.results || []; n.full = true;
    } catch { /* hub offline */ }
  }
  if (!S.detail || S.detail.id !== id) return;
  const st = n.static || {}, D = S.detail;
  const act = (icon, label, opts) => h("button", { class: "btn sm " + (opts.cls || ""), disabled: opts.disabled, title: opts.title || "", onclick: opts.onclick }, ic(icon), h("span", null, label));
  const noManage = !st.manage ? "Install this agent with --manage to enable" : "";
  const actions = h("div", { class: "actions" },
    act("term", "Console", { disabled: !st.manage, title: noManage, onclick: () => (location.hash = `#/n/${id}/console`) }),
    act("reboot", "Reboot", { cls: "danger", disabled: !st.manage, title: noManage, onclick: () => confirmAct("Reboot " + title(n) + "?", "The machine will restart immediately.", () => sendCmd(id, "reboot")) }),
    act("power", "Shut down", { cls: "danger", disabled: !st.manage, title: noManage, onclick: () => confirmAct("Shut down " + title(n) + "?", "You'll need physical or out-of-band access to power it back on.", () => sendCmd(id, "shutdown")) }),
    act("trash", "Forget", { cls: "ghost", title: "Remove from Argus and revoke this agent's key", onclick: () => confirmAct("Forget " + title(n) + "?", "Its agent key is revoked. It can only come back by enrolling again with the enroll token.", async () => { await api("/api/node/" + id, { method: "DELETE" }); S.nodes.delete(id); location.hash = "#/"; }) }));
  const counts = { processes: n.last?.nprocs, devices: (st.devices || []).length, network: (n.last?.net || []).length, containers: ctrCount(n.last)[1] };
  D.refs.dot = h("span", { class: "dot" }); D.refs.sub = h("div", { class: "sub" });
  $("#main").replaceChildren(
    h("a", { class: "back", href: "#/" }, ic("back"), "Fleet"),
    h("div", { class: "dhead" }, osIcon(st.os),
      h("div", { class: "who" }, h("h1", null, D.refs.dot, title(n), h("span", { class: "tag" }, ic("cpu"), st.arch || ""), st.virt ? h("span", { class: "tag warn" }, ic("monitor"), st.virt) : null), D.refs.sub),
      actions),
    h("div", { class: "tabs" }, TABS.filter(([t]) => t !== "containers" || n.last?.docker || st.extra?.["Container engine"]).map(([t, label, icon]) =>
      h("button", { class: "tab" + (t === tab ? " on" : ""), onclick: () => (location.hash = `#/n/${id}/${t}`) }, ic(icon), label, counts[t] ? h("span", { class: "count" }, counts[t]) : null))),
    (D.body = h("div")));
  ({ overview: buildOverview, containers: buildContainers, processes: buildProcesses, hardware: buildHardware, devices: buildDevices, network: buildNetwork, console: buildConsole })[tab](n);
  updateDetail(n, true);
}

function updateDetail(n, force) {
  const D = S.detail; if (!D || D.id !== n.id) return;
  const s = status(n), m = n.last, st = n.static || {};
  D.refs.dot.className = "dot " + (s === "on" ? "" : s);
  const bits = [[st.os === "windows" ? "monitor" : "server", subtitle(st)], ["globe", n.addr],
    [s === "off" ? "alert" : "clock", s === "off" ? "last seen " + fmt.ago(now() - n.seen) : "up " + fmt.dur(m?.uptime)],
    s === "on" ? ["activity", "live · every " + ((n.iv || 2000) / 1000) + "s"] : null,
    m?.self ? ["eye", `agent ${fmt.pct(m.self.cpu)}% CPU · ${fmt.bytes(m.self.rss, 0)}`] : null];
  const key = JSON.stringify(bits);
  if (D.refs.sub.dataset.k !== key) { D.refs.sub.dataset.k = key; D.refs.sub.replaceChildren(...bits.filter((b) => b && b[1]).map(([i, t]) => h("span", null, ic(i, "sm"), t))); }
  if (D.update) D.update(n, force);
}

function kpi(label, color, icon, gauge) {
  const v = h("div", { class: "v" }, "–"), s = h("div", { class: "s" });
  const vis = gauge ? ring(color, icon) : h("div", { class: "icobox", style: { background: `color-mix(in srgb, var(${color}) 14%, transparent)`, color: `var(${color})` } }, ic(icon, "lg"));
  return { el: h("div", { class: "kpi" }, vis, h("div", { class: "body" }, h("label", null, label), v, s)), v, s, ring: gauge ? vis : null };
}

function rangeData(n, range) {
  if (range === "24h") return (n.slow || []).concat((n.hist || []).slice(-1));
  const hist = n.hist || [];
  const cut = (hist.length ? hist[hist.length - 1][0] : 0) - (range === "1h" ? 3600 : 300);
  return hist.filter((p) => p[0] >= cut);
}

function buildOverview(n) {
  const D = S.detail, st = n.static || {};
  const K = { cpu: kpi("CPU", "--cpu", "cpu", true), mem: kpi("Memory", "--mem", "mem", true), gpu: kpi("GPU", "--gpu", "gpu", true), net: kpi("Network", "--rx", "net"), disk: kpi("Disk I/O", "--dr", "disk"), sys: kpi("System", "--hot", "zap") };
  const seg = h("div", { class: "seg" }, ["5m", "1h", "24h"].map((r) => h("button", { class: r === D.range ? "on" : "", onclick: (e) => { D.range = r; $$("button", seg).forEach((b) => b.classList.toggle("on", b === e.currentTarget)); D.update(S.nodes.get(n.id), true); } }, r)));
  const mk = (cls, opts) => { const c = h("canvas", { class: "chart " + (cls || "") }), lg = h("div", { class: "legend" }); return [c, lg, new Chart(c, { ...opts, legend: lg })]; };
  const [c1, l1, util] = mk("tall", { max: 100, fmtY: (v) => Math.round(v) + "%", series: [{ i: 1, color: "--cpu", label: "CPU" }, { i: 2, color: "--mem", label: "Memory" }, { i: 3, color: "--gpu", label: "GPU" }] });
  const [c2, l2, net] = mk("", { fmtY: (v) => fmt.bits(v), series: [{ i: 4, color: "--rx", label: "Receive" }, { i: 5, color: "--tx", label: "Transmit" }] });
  const [c3, l3, disk] = mk("", { fmtY: (v) => fmt.rate(v), series: [{ i: 6, color: "--dr", label: "Read" }, { i: 7, color: "--dw", label: "Write" }] });
  const [c4, l4, temp] = mk("", { fmtY: (v) => Math.round(v) + "°", series: [{ i: 8, color: "--hot", label: "Hottest sensor" }] });
  Object.assign(D.charts, { util, net, disk, temp });
  const R = D.refs;
  R.cores = h("div", { class: "cores" }); R.mem = h("div"); R.gpus = h("div"); R.fs = h("div", { class: "rows" }); R.sensors = h("div", { class: "rows" }); R.top = h("div", { class: "rows" }); R.ctrs = h("div", { class: "rows" });
  const hasTemp = () => (n.hist || []).some((p) => p[8] > 0);
  D.body.replaceChildren(
    h("div", { class: "kpis" }, Object.values(K).map((k) => k.el)),
    h("div", { class: "panels" },
      h("div", { class: "panel span8" }, h("h4", null, ic("overview"), "Utilization", h("span", { class: "right" }, seg)), c1, l1),
      h("div", { class: "panel span4" }, h("h4", null, ic("cpu"), "Cores", (R.coreInfo = h("span", { class: "right faint" }))), R.cores),
      h("div", { class: "panel span6" }, h("h4", null, ic("net"), "Network"), c2, l2),
      h("div", { class: "panel span6" }, h("h4", null, ic("disk"), "Disk I/O"), c3, l3),
      h("div", { class: "panel span4" }, h("h4", null, ic("mem"), "Memory"), R.mem),
      h("div", { class: "panel span4" }, h("h4", null, ic("gpu"), "Graphics", h("span", { class: "right faint" }, (st.gpus || []).length + " device(s)")), R.gpus),
      h("div", { class: "panel span4" }, h("h4", null, ic("disk"), "Storage"), R.fs),
      (R.ctrPanel = h("div", { class: "panel span4" }, h("h4", null, ic("docker"), "Containers", h("a", { class: "right muted", href: `#/n/${n.id}/containers` }, "all", ic("back", "sm"))), R.ctrs)),
      h("div", { class: "panel span4" }, h("h4", null, ic("activity"), "Top processes", h("a", { class: "right muted", href: `#/n/${n.id}/processes` }, "all")), R.top),
      h("div", { class: "panel span4" }, h("h4", null, ic("temp"), "Sensors & power"), R.sensors),
      (R.tempPanel = h("div", { class: "panel span12" }, h("h4", null, ic("temp"), "Temperature"), c4, l4))));
  $("svg", R.ctrPanel.querySelector(".right")).style.transform = "rotate(180deg)";
  let lastHeavy = 0;
  D.update = (n, force) => {
    const m = n.last || {}, st = n.static || {};
    const data = rangeData(n, D.range);
    util.set(data); net.set(data); disk.set(data); temp.set(data);
    R.tempPanel.style.display = hasTemp() ? "" : "none";
    K.cpu.v.replaceChildren(fmt.pct(m.cpu), h("small", null, "%")); K.cpu.ring.set(m.cpu);
    K.cpu.s.textContent = [m.mhz ? fmt.mhz(m.mhz) : null, m.load ? "load " + m.load.map((x) => x.toFixed(2)).join(" ") : null].filter(Boolean).join(" · ") || st.cpuModel || "";
    K.mem.v.replaceChildren(fmt.pct(memPct(m)), h("small", null, "%")); K.mem.ring.set(memPct(m));
    K.mem.s.textContent = `${fmt.bytes(m.mem?.used)} of ${fmt.bytes(m.mem?.total)}` + (m.mem?.swapUsed ? ` · swap ${fmt.bytes(m.mem.swapUsed)}` : "");
    const gp = gpuPct(m);
    K.gpu.v.replaceChildren(gp == null ? "–" : fmt.pct(gp), h("small", null, gp == null ? "" : "%")); K.gpu.ring.set(gp);
    K.gpu.s.textContent = (m.gpus || []).map((g) => g.name).join(", ") || (st.gpus || []).map((g) => g.name).join(", ") || "none detected";
    const [rx, tx] = netSum(n);
    K.net.v.replaceChildren(...valUnit(fmt.bits(rx))); K.net.s.textContent = "↓ in · ↑ " + fmt.bits(tx) + (m.tcp?.established ? ` · ${m.tcp.established} TCP` : "");
    let dr = 0, dw = 0; for (const d of m.disks || []) { dr += d.r; dw += d.w; }
    K.disk.v.replaceChildren(...valUnit(fmt.rate(dr + dw))); K.disk.s.textContent = `read ${fmt.rate(dr)} · write ${fmt.rate(dw)}`;
    if (m.sysW) K.sys.v.replaceChildren(String(m.sysW), h("small", null, "W"));
    else if (maxTemp(m)) K.sys.v.replaceChildren(String(Math.round(maxTemp(m))), h("small", null, "°C"));
    else K.sys.v.replaceChildren(fmt.dur(m.uptime));
    K.sys.s.textContent = [m.nprocs ? m.nprocs + " procs" : null, "up " + fmt.dur(m.uptime), m.thermal ? "thermal " + m.thermal : null].filter(Boolean).join(" · ");

    // cores (E-cores tinted on Apple Silicon: macOS numbers them first)
    const cores = m.cores || [];
    if (R.cores.children.length !== cores.length) R.cores.replaceChildren(...cores.map((_, i) => h("div", { class: "core", title: "core " + i }, h("i"), h("span"))));
    cores.forEach((v, i) => {
      const el = R.cores.children[i];
      el.firstChild.style.height = v + "%";
      el.firstChild.style.background = v > 90 ? "var(--hot)" : st.os === "darwin" && st.eCores && i < st.eCores ? "var(--ctr)" : "var(--cpu)";
      el.lastChild.textContent = Math.round(v);
    });
    R.coreInfo.textContent = st.pCores ? `${st.pCores} perf + ${st.eCores} eff` : `${st.cpuCores || "?"} cores · ${st.cpuThreads} threads`;

    // memory
    const mm = m.mem || {}, tot = mm.total || 1;
    const segs = mm.wired || mm.compressed
      ? [["App", (mm.used || 0) - (mm.wired || 0) - (mm.compressed || 0), "--mem"], ["Wired", mm.wired, "--cpu"], ["Compressed", mm.compressed, "--dw"], ["Cached", mm.cached, "--faint"]]
      : [["Used", mm.used, "--mem"], ["Cache", Math.min(mm.cached || 0, mm.avail || 0), "--faint"]];
    R.mem.replaceChildren(
      h("div", { class: "membar" }, segs.map(([l, v, c]) => h("i", { title: `${l} ${fmt.bytes(v)}`, style: { width: Math.max(0, (v / tot) * 100) + "%", background: `var(${c})`, opacity: c === "--faint" ? 0.45 : 1 } }))),
      h("dl", { class: "kv" },
        segs.flatMap(([l, v, c]) => [h("dt", null, h("i", { class: "dot", style: { background: `var(${c})`, boxShadow: "none" } }), l), h("dd", { class: "num" }, fmt.bytes(Math.max(0, v || 0)))]),
        h("dt", null, "Available"), h("dd", { class: "num" }, fmt.bytes(mm.avail)),
        h("dt", null, "Swap"), h("dd", { class: "num" }, mm.swapTotal ? `${fmt.bytes(mm.swapUsed)} of ${fmt.bytes(mm.swapTotal)}` : "none"),
        mm.pressure ? [h("dt", null, "Pressure"), h("dd", { class: "num" }, mm.pressure.toFixed(1) + "%")] : null));

    // GPUs
    const gs = m.gpus?.length ? m.gpus : (st.gpus || []).map((g) => ({ name: g.name }));
    R.gpus.replaceChildren(...gs.map((g, i) => {
      const info = (st.gpus || []).find((x) => x.name === g.name) || (st.gpus || [])[i] || {};
      const stat = (l, v) => (v ? h("div", null, h("label", null, l), h("b", null, v)) : null);
      return h("div", { class: "gpu-card" },
        h("h5", null, ic("gpu"), g.name), h("div", { class: "faint", style: { fontSize: "12px" } }, [info.vendor, info.driver && `${info.driver} ${info.driverVersion || ""}`, info.api].filter(Boolean).join(" · ")),
        h("div", { class: "bar" }, h("i", { style: { width: (g.util || 0) + "%", background: "var(--gpu)" } })),
        h("div", { class: "gpu-stats" },
          stat("Util", g.util != null ? fmt.pct(g.util) + "%" : null),
          stat("VRAM", g.memUsed ? fmt.bytes(g.memUsed) + (g.memTotal ? " / " + fmt.bytes(g.memTotal, 0) : "") : info.vram ? fmt.bytes(info.vram, 0) : st.os === "darwin" ? "unified" : null),
          stat("Temp", g.temp ? Math.round(g.temp) + "°C" : null), stat("Power", g.watts ? g.watts + " W" : null),
          stat("Clock", g.mhz ? fmt.mhz(g.mhz) : null), stat("Fan", g.fan ? Math.round(g.fan) + (g.fan > 100 ? " rpm" : "%") : null), stat("Cores", info.cores || null)));
    }));
    if (!gs.length) R.gpus.append(h("div", { class: "muted" }, "No GPU detected"));

    R.fs.replaceChildren(...(m.fs || []).map((f) => {
      const p = f.total ? (f.used / f.total) * 100 : 0;
      return h("div", { class: "fsrow" }, h("span", { class: "name mono", title: `${f.dev} (${f.type})` }, f.mount),
        h("span", { class: "track" }, h("i", { style: { width: p + "%", background: barColor(p) || "var(--dr)" } })),
        h("span", { class: "num muted", style: { minWidth: "112px", textAlign: "right" } }, `${fmt.bytes(f.used, 0)} / ${fmt.bytes(f.total, 0)}`));
    }));

    const rows = [];
    if (m.self) rows.push(["eye", "Argus agent", `${fmt.pct(m.self.cpu)}% · ${fmt.bytes(m.self.rss, 0)}`]);
    if (m.cpuW) rows.push(["zap", "CPU package power", m.cpuW + " W"]);
    if (m.sysW) rows.push(["zap", "SoC combined power", m.sysW + " W"]);
    if (m.thermal) rows.push(["temp", "Thermal pressure", m.thermal]);
    if (m.battery) rows.push(["battery", "Battery", `${Math.round(m.battery.pct)}% · ${m.battery.state || (m.battery.charging ? "charging" : "")}`]);
    for (const t of m.temps || []) rows.push(/power/i.test(t.name) ? ["zap", t.name, t.v + " W"] : ["temp", t.name, t.v.toFixed(1) + " °C", t.v > 85]);
    for (const f of m.fans || []) rows.push(["fan", f.name, Math.round(f.v) + " rpm"]);
    R.sensors.replaceChildren(...rows.slice(0, 24).map(([i, a, b, hot]) => h("div", null, h("span", { class: "name" }, ic(i, "sm"), a), h("span", { class: "num", style: hot ? { color: "var(--hot)" } : null }, b))));

    const cs = (m.docker?.containers || []).filter((c) => c.state === "running").sort((a, b) => b.cpu - a.cpu).slice(0, 6);
    R.ctrPanel.style.display = m.docker ? "" : "none";
    if (m.docker) R.ctrs.replaceChildren(...(cs.length ? cs.map((c) => h("div", null, h("span", { class: "name" }, h("span", { class: "dot " + healthDot(c) }), c.name),
      h("span", { class: "num muted" }, fmt.bytes(c.mem, 0)), h("span", { class: "num", style: { minWidth: "52px", textAlign: "right", color: "var(--ctr)" } }, fmt.pct(c.cpu) + "%"))) : [h("div", { class: "muted" }, "No running containers")]));

    if (m.procs && (force || Date.now() - lastHeavy > 2500)) {
      lastHeavy = Date.now();
      R.top.replaceChildren(...[...m.procs].sort((a, b) => b.cpu - a.cpu).slice(0, 7).map((p) => h("div", null,
        h("span", { class: "name" }, p.name), h("span", { class: "num muted" }, fmt.bytes(p.mem, 0)), h("span", { class: "num", style: { minWidth: "52px", textAlign: "right", color: "var(--cpu)" } }, fmt.pct(p.cpu) + "%"))));
    }
  };
}

const healthDot = (c) => (c.state !== "running" ? (c.state === "restarting" ? "warn" : "off") : c.health === "unhealthy" ? "bad" : c.health === "starting" ? "warn" : "");

function buildContainers(n) {
  const D = S.detail, st = n.static || {};
  const out = h("div"), summary = h("div", { class: "ctr-summary" });
  const q = h("input", { placeholder: "Filter containers, images, projects…", oninput: () => D.update(S.nodes.get(n.id), true) });
  const seg = h("div", { class: "seg" }, [["all", "All"], ["running", "Running"], ["stopped", "Stopped"]].map(([k, l]) => h("button", { class: D.ctrFilter === k ? "on" : "", onclick: (e) => { D.ctrFilter = k; $$("button", seg).forEach((b) => b.classList.toggle("on", b === e.currentTarget)); D.update(S.nodes.get(n.id), true); } }, l)));
  D.body.replaceChildren(summary, h("div", { class: "filterbar" }, h("label", { class: "search" }, ic("search"), q), seg), out);
  const rowsById = new Map();
  const cact = (icon, label, op, c, cls) => h("button", { class: "btn icon sm ghost " + (cls || ""), title: label, "aria-label": label, disabled: !st.manage, onclick: (e) => {
    e.stopPropagation();
    if (op === "stop" || op === "restart" || op === "kill") confirmAct(`${label} ${c.name}?`, `${c.image} on ${title(n)}`, () => sendCmd(n.id, "docker", op + " " + c.id));
    else sendCmd(n.id, "docker", op + " " + c.id, op === "logs" || op === "inspect" ? { view: `${c.name} · ${op}` } : null);
  } }, ic(icon));
  function row(c) {
    const r = {};
    r.el = h("div", { class: "ctr" },
      h("div", { class: "nm" }, h("b", null, (r.dot = h("span", { class: "dot" })), (r.name = h("span"))), (r.img = h("small"))),
      h("div", { class: "metric m-cpu" }, h("small", null, "CPU"), (r.cpu = h("span")), h("div", { class: "bar" }, (r.cpuBar = h("i", { style: { background: "var(--ctr)" } })))),
      h("div", { class: "metric m-mem" }, h("small", null, "Memory"), (r.mem = h("span")), h("div", { class: "bar" }, (r.memBar = h("i", { style: { background: "var(--mem)" } })))),
      h("div", { class: "metric m-net" }, h("small", null, "Network"), (r.net = h("span", { class: "net2" }))),
      (r.ports = h("div", { class: "ports" })),
      (r.acts = h("div", { class: "acts" })));
    return r;
  }
  D.update = (n) => {
    const d = n.last?.docker;
    if (!d) { out.replaceChildren(h("div", { class: "empty" }, "No container engine detected on this machine.")); return; }
    const all = d.containers || [], run = all.filter((c) => c.state === "running");
    let cpu = 0, mem = 0; for (const c of run) { cpu += c.cpu; mem += c.mem; }
    const p = (icon, ...k) => h("span", { class: "pill" }, ic(icon), ...k);
    summary.replaceChildren(p("docker", h("b", null, d.engine), d.version), p("play", h("b", null, `${run.length}/${all.length}`), "running"),
      p("layers", h("b", null, d.images), "images"), p("cpu", h("b", null, fmt.pct(cpu) + "%"), "CPU"), p("mem", h("b", null, fmt.bytes(mem, 0)), "memory"),
      d.driver ? p("disk", d.driver) : null, d.error ? h("span", { class: "tag bad" }, ic("alert"), d.error) : null);
    const qq = q.value.toLowerCase();
    const list = all.filter((c) => (D.ctrFilter === "all" || (D.ctrFilter === "running") === (c.state === "running")) &&
      (!qq || [c.name, c.image, c.project, c.service, c.status, ...(c.ports || [])].join(" ").toLowerCase().includes(qq)));
    const groups = new Map();
    for (const c of list) { const g = c.project || ""; if (!groups.has(g)) groups.set(g, []); groups.get(g).push(c); }
    const key = [...groups.entries()].map(([g, cs]) => g + ":" + cs.map((c) => c.id + c.state).join(",")).join("|") + st.manage;
    if (out.dataset.k !== key) {
      out.dataset.k = key; rowsById.clear();
      out.replaceChildren(...[...groups.entries()].sort((a, b) => (a[0] === "") - (b[0] === "") || a[0].localeCompare(b[0])).map(([g, cs]) =>
        h("div", { class: "list ctr-group" },
          h("div", { class: "ctr-group-h" }, ic(g ? "layers" : "box"), g || "Standalone containers", h("span", { class: "tag" }, `${cs.filter((c) => c.state === "running").length}/${cs.length}`)),
          cs.map((c) => { const r = row(c); rowsById.set(c.id, r); return r.el; }))));
      if (!list.length) out.replaceChildren(h("div", { class: "empty" }, all.length ? "No containers match." : "No containers on this machine."));
    }
    for (const c of list) {
      const r = rowsById.get(c.id); if (!r) continue;
      const running = c.state === "running";
      r.el.classList.toggle("stopped", !running);
      r.dot.className = "dot " + healthDot(c); r.name.textContent = c.service && c.project ? c.service : c.name;
      r.img.textContent = `${c.image} · ${c.status}`; r.img.title = `${c.name} (${c.id})`;
      r.cpu.textContent = running ? fmt.pct(c.cpu) + "%" : "–"; r.cpuBar.style.width = Math.min(100, c.cpu) + "%";
      r.mem.textContent = running ? fmt.bytes(c.mem) + (c.memLimit ? " / " + fmt.bytes(c.memLimit, 0) : "") : "–";
      r.memBar.style.width = (c.memLimit ? (c.mem / c.memLimit) * 100 : 0) + "%";
      r.net.replaceChildren(h("span", { class: "rx" }, "↓ " + fmt.bits(c.rx)), " ", h("span", { class: "tx" }, "↑ " + fmt.bits(c.tx)));
      const pk = (c.ports || []).join() + (c.health || "");
      if (r.ports.dataset.k !== pk) {
        r.ports.dataset.k = pk;
        r.ports.replaceChildren(...(c.ports || []).slice(0, 4).map((p) => h("span", { class: "tag" }, ic("plug"), p)), c.health ? h("span", { class: "tag " + (c.health === "healthy" ? "ok" : c.health === "unhealthy" ? "bad" : "warn") }, ic("heart"), c.health) : null);
      }
      const ak = c.state + st.manage;
      if (r.acts.dataset.k !== ak) {
        r.acts.dataset.k = ak;
        r.acts.replaceChildren(
          running ? cact("restart", "Restart", "restart", c) : cact("play", "Start", "start", c),
          running ? cact("stop", "Stop", "stop", c, "danger") : null,
          cact("logs", "Logs", "logs", c), cact("inspect", "Inspect", "inspect", c));
      }
    }
  };
}

function buildProcesses(n) {
  const D = S.detail, st = n.static || {};
  const tbody = h("tbody"), count = h("span", { class: "muted" });
  const filter = h("input", { placeholder: "Filter processes…", oninput: () => D.update(S.nodes.get(n.id), true) });
  const cols = [["pid", "PID"], ["name", "Name"], ["user", "User", "hide-sm"], ["cpu", "CPU"], ["mem", "Memory"]];
  const ths = cols.map(([k, l, c]) => h("th", { class: "sort " + (c || "") + (D.procSort === k ? " sorted" : ""), onclick: () => { D.procSort = k; ths.forEach((t, i) => t.classList.toggle("sorted", cols[i][0] === k)); D.update(S.nodes.get(n.id), true); } }, l));
  D.body.replaceChildren(h("div", { class: "filterbar" }, h("label", { class: "search" }, ic("search"), filter), count, h("span", { class: "faint hide-sm" }, "· top consumers, live")),
    h("div", { class: "list scroll" }, h("table", null, h("thead", null, h("tr", null, ths, h("th"))), tbody)));
  let last;
  D.update = (n, force) => {
    const m = n.last || {};
    if (!force && m.procs === last) return;
    if (m.procs) last = m.procs;
    const q = filter.value.toLowerCase(), k = D.procSort;
    const ps = (last || []).filter((p) => !q || (p.name + " " + p.pid + " " + (p.user || "")).toLowerCase().includes(q))
      .sort((a, b) => (k === "name" || k === "user" ? String(a[k] || "").localeCompare(String(b[k] || "")) : b[k] - a[k]));
    count.textContent = `${m.nprocs || ps.length} processes`;
    tbody.replaceChildren(...ps.map((p) => h("tr", null,
      h("td", { class: "num muted" }, p.pid), h("td", null, p.name), h("td", { class: "muted hide-sm" }, p.user || ""),
      h("td", { class: "num" }, h("span", { class: "mini-bar" }, h("i", { style: { width: Math.min(100, p.cpu) + "%", background: "var(--cpu)" } })), fmt.pct(p.cpu) + "%"),
      h("td", { class: "num" }, fmt.bytes(p.mem)),
      h("td", null, st.manage ? h("button", { class: "btn icon sm ghost danger", title: "Kill", "aria-label": "Kill " + p.name, onclick: () => confirmAct(`Kill ${p.name} (${p.pid})?`, "on " + title(n), () => sendCmd(n.id, "kill", String(p.pid))) }, ic("x")) : null))));
  };
}

function kvList(pairs) {
  return h("dl", { class: "kv" }, pairs.filter(([, v]) => v !== undefined && v !== null && v !== "" && v !== 0).flatMap(([k, v]) => [h("dt", null, k), h("dd", null, v)]));
}
const tbl = (cols, rows) => (rows.length ? h("div", { class: "list" }, h("table", null, h("thead", null, h("tr", null, cols.map((c) => h("th", null, c)))), h("tbody", null, rows.map((r) => h("tr", null, r.map((c) => h("td", { class: "wrap" }, c ?? ""))))))) : h("div", { class: "muted" }, "None reported"));

function buildHardware(n) {
  const D = S.detail, st = n.static || {};
  D.body.replaceChildren(h("div", { class: "panels" },
    h("div", { class: "panel span6" }, h("h4", null, ic("server"), "System"), kvList([
      ["Hostname", st.hostname], ["Platform", st.platform], ["Kernel", st.kernel], ["Architecture", st.arch], ["Model", st.model], ["Vendor", st.vendor],
      ["Firmware", st.firmware], ["Virtualization", st.virt], ["Booted", st.bootTime ? new Date(st.bootTime * 1000).toLocaleString() : ""],
      ["Timezone", st.tz], ["Address", n.addr], ["Machine ID", h("span", { class: "mono faint" }, st.id)], ["Agent", `argus ${st.agent}${st.manage ? " · managed" : " · read-only"}`],
      ...Object.entries(st.extra || {}).sort(),
    ])),
    h("div", { class: "panel span6" }, h("h4", null, ic("cpu"), "Processor & memory"), kvList([
      ["CPU", st.cpuModel], ["Cores", st.pCores ? `${st.cpuCores} (${st.pCores} performance + ${st.eCores} efficiency)` : st.cpuCores], ["Threads", st.cpuThreads],
      ["Max clock", st.cpuMaxMHz ? fmt.mhz(st.cpuMaxMHz) : ""], ["ISA / features", st.cpuFlags ? h("span", { class: "mono", style: { fontSize: "12px" } }, st.cpuFlags) : ""], ["Memory", fmt.bytes(st.memTotal)],
    ])),
    h("div", { class: "panel span12" }, h("h4", null, ic("gpu"), "Graphics"), tbl(["Name", "Vendor", "Driver", "Version", "Memory", "Cores", "API", "Bus", "Details"],
      (st.gpus || []).map((g) => [h("b", null, g.name), g.vendor, g.driver, h("span", { class: "mono" }, g.driverVersion || ""), g.vram ? fmt.bytes(g.vram, 0) : "", g.cores || "", g.api, h("span", { class: "mono faint" }, g.bus || ""), g.extra]))),
    h("div", { class: "panel span6" }, h("h4", null, ic("disk"), "Storage devices"), tbl(["Device", "Model", "Type", "Size"], (st.storage || []).map((d) => [h("span", { class: "mono" }, d.name), d.model, d.kind, fmt.bytes(d.size, 0)]))),
    h("div", { class: "panel span6" }, h("h4", null, ic("net"), "Network interfaces"), tbl(["Name", "Addresses", "Driver / port", "Speed", "MAC"],
      (st.nics || []).filter((x) => !x.virt || x.addrs?.length).map((x) => [h("span", { style: { display: "inline-flex", gap: "8px", alignItems: "center" } }, h("span", { class: "dot " + (x.up ? "" : "off") }), x.name), h("span", { class: "mono", style: { fontSize: "12px" } }, (x.addrs || []).join("\n")), x.driver, x.speed ? (x.speed >= 1000 ? x.speed / 1000 + " Gb/s" : x.speed + " Mb/s") : "", h("span", { class: "mono faint" }, x.mac || "")])))));
  D.update = null;
}

function buildDevices(n) {
  const D = S.detail, st = n.static || {};
  let mode = "Devices";
  const filter = h("input", { placeholder: "Search devices & drivers…", oninput: draw }), count = h("span", { class: "faint" }), out = h("div");
  const seg = h("div", { class: "seg" }, [["Devices", "usb"], ["Drivers", "puzzle"]].map(([t, i], k) => h("button", { class: k === 0 ? "on" : "", onclick: (e) => { $$("button", seg).forEach((b) => b.classList.toggle("on", b === e.currentTarget)); mode = t; draw(); } }, ic(i, "sm"), t)));
  D.body.replaceChildren(h("div", { class: "filterbar" }, seg, h("label", { class: "search" }, ic("search"), filter), count), out);
  function draw() {
    const q = filter.value.toLowerCase(), match = (o) => !q || Object.values(o).join(" ").toLowerCase().includes(q);
    if (mode === "Devices") {
      const ds = (st.devices || []).filter(match); count.textContent = ds.length + " devices";
      out.replaceChildren(h("div", { class: "list scroll" }, h("table", null, h("thead", null, h("tr", null, ["Bus", "Class", "Device", "Vendor", "ID", "Driver"].map((c, i) => h("th", { class: i === 1 || i === 4 ? "hide-sm" : "" }, c)))),
        h("tbody", null, ds.map((d) => h("tr", null, h("td", null, h("span", { class: "tag" }, d.bus)), h("td", { class: "muted hide-sm" }, d.class || ""), h("td", { class: "wrap" }, d.name), h("td", { class: "muted wrap" }, d.vendor || ""), h("td", { class: "mono faint hide-sm" }, d.id || ""), h("td", { class: "mono" }, d.driver || "")))))));
    } else {
      const ds = (st.drivers || []).filter(match); count.textContent = ds.length + " drivers";
      out.replaceChildren(h("div", { class: "list scroll" }, h("table", null, h("thead", null, h("tr", null, ["Driver / module", "Version", "Details"].map((c) => h("th", null, c)))),
        h("tbody", null, ds.map((d) => h("tr", null, h("td", { class: "mono" }, d.name), h("td", { class: "mono" }, d.version || ""), h("td", { class: "muted wrap" }, d.info || "")))))));
    }
  }
  draw();
  D.update = null;
}

function buildNetwork(n) {
  const D = S.detail;
  const c = h("canvas", { class: "chart tall" }), lg = h("div", { class: "legend" });
  D.charts.net = new Chart(c, { fmtY: (v) => fmt.bits(v), legend: lg, series: [{ i: 4, color: "--rx", label: "Receive" }, { i: 5, color: "--tx", label: "Transmit" }] });
  const tbody = h("tbody"), tcp = h("div", { class: "chips" });
  D.body.replaceChildren(h("div", { class: "panels" },
    h("div", { class: "panel span12" }, h("h4", null, ic("net"), "Throughput · physical interfaces"), c, lg),
    h("div", { class: "panel span12" }, h("h4", null, ic("globe"), "Interfaces"), h("div", { class: "list" }, h("table", null,
      h("thead", null, h("tr", null, ["Interface", "Receive", "Transmit", "Rx pkt/s", "Tx pkt/s", "Total rx", "Total tx", "Errors", "Addresses"].map((x, i) => h("th", { class: i >= 3 && i !== 5 && i !== 6 ? "hide-sm" : "" }, x)))), tbody))),
    h("div", { class: "panel span12" }, h("h4", null, ic("plug"), "TCP connections"), tcp)));
  D.update = (n) => {
    const m = n.last || {}, st = n.static || {};
    D.charts.net.set(rangeData(n, "1h"));
    const nic = Object.fromEntries((st.nics || []).map((x) => [x.name, x]));
    const nets = [...(m.net || [])].sort((a, b) => (nic[a.name]?.virt ? 1 : 0) - (nic[b.name]?.virt ? 1 : 0) || b.rx + b.tx - (a.rx + a.tx));
    tbody.replaceChildren(...nets.map((x) => h("tr", { style: nic[x.name]?.virt ? { opacity: 0.6 } : null },
      h("td", null, h("b", null, x.name), nic[x.name]?.virt ? h("span", { class: "tag", style: { marginLeft: "8px" } }, "virtual") : null),
      h("td", { class: "num", style: { color: "var(--rx)" } }, fmt.bits(x.rx)), h("td", { class: "num", style: { color: "var(--tx)" } }, fmt.bits(x.tx)),
      h("td", { class: "num muted hide-sm" }, Math.round(x.rxP || 0)), h("td", { class: "num muted hide-sm" }, Math.round(x.txP || 0)),
      h("td", { class: "num" }, fmt.bytes(x.rxT)), h("td", { class: "num" }, fmt.bytes(x.txT)), h("td", { class: "num hide-sm" + (x.errs ? "" : " faint") }, x.errs || 0),
      h("td", { class: "mono muted wrap hide-sm", style: { fontSize: "12px" } }, (nic[x.name]?.addrs || []).join("  ")))));
    tcp.replaceChildren(...(m.tcp ? Object.entries(m.tcp).sort((a, b) => b[1] - a[1]).map(([k, v]) => h("span", { class: "pill" }, h("b", null, v), k.replace("_", " "))) : [h("span", { class: "muted" }, "Not reported on this platform")]));
  };
}

const QUICK = {
  linux: ["uptime", "df -h", "free -h", "ip -br addr", "docker ps --format 'table {{.Names}}\\t{{.Status}}\\t{{.Ports}}'", "ss -tlnp", "systemctl --failed", "journalctl -p err -n 30 --no-pager", "nvidia-smi", "sensors"],
  darwin: ["uptime", "df -h", "vm_stat", "docker ps", "ifconfig | grep 'inet '", "softwareupdate -l", "pmset -g batt", "system_profiler SPPowerDataType"],
  windows: ["Get-ComputerInfo | Select OsName,OsVersion,CsModel", "Get-Volume", "Get-NetAdapter", "docker ps", "Get-Service | ? Status -eq Running | select -First 30", "Get-HotFix | sort InstalledOn -desc | select -First 10", "nvidia-smi"],
};

function buildConsole(n) {
  const D = S.detail, st = n.static || {}, ps1 = st.os === "windows";
  const out = h("div", { class: "out" }), input = h("input", { placeholder: st.manage ? (ps1 ? "PowerShell command…" : "shell command…") : "management is disabled on this agent", disabled: !st.manage, autocomplete: "off", spellcheck: "false", "aria-label": "Command" });
  const hist = []; let hi = 0;
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowUp" && hist.length) { hi = Math.max(0, hi - 1); input.value = hist[hi]; e.preventDefault(); }
    if (e.key === "ArrowDown" && hist.length) { hi = Math.min(hist.length, hi + 1); input.value = hist[hi] || ""; e.preventDefault(); }
  });
  const run = (cmd) => sendCmd(n.id, "exec", cmd);
  const form = h("form", { onsubmit: (e) => { e.preventDefault(); const v = input.value.trim(); if (!v) return; hist.push(v); hi = hist.length; input.value = ""; run(v); } }, h("span", { class: "prompt" }, ps1 ? "PS>" : "$"), input);
  D.body.replaceChildren(
    st.manage ? h("div", { class: "quick" }, (QUICK[st.os] || QUICK.linux).map((q) => h("button", { class: "btn sm", onclick: () => run(q) }, ic("term", "sm"), h("span", { class: "mono", style: { fontSize: "11.5px" } }, q.length > 42 ? q.slice(0, 40) + "…" : q))))
      : h("div", { class: "notice" }, ic("lock"), h("div", null, h("b", null, "Remote management is off for this machine. "), h("span", { class: "muted" }, "Reinstall the agent with --manage (Windows: tick “Allow remote management” in Add machine) to enable the console, containers, kill, reboot and shutdown."))),
    h("div", { class: "console" }, out, form));
  D.update = (n, force) => {
    const rs = (n.results || []).filter((r) => r.kind === "exec" || r.kind === "kill" || r.kind === "reboot" || r.kind === "shutdown");
    const pend = [...S.pending.values()].filter((p) => p.node === n.id && p.kind === "exec");
    const k = rs.length + "|" + pend.length;
    if (!force && out.dataset.n === k) return;
    out.dataset.n = k;
    out.replaceChildren(...rs.map((r) => h("div", { class: "entry" },
      h("div", { class: "cmdline" }, r.kind === "exec" ? `${ps1 ? "PS>" : "$"} ${r.arg}` : `[${r.kind}${r.arg ? " " + r.arg : ""}]`, h("span", { class: "meta" }, `${fmt.time(r.at)} · ${r.ms} ms · exit ${r.code}`)),
      h("pre", { class: r.ok ? "" : "err" }, r.output || "(no output)"))),
      ...pend.map((p) => h("div", { class: "entry" }, h("div", { class: "cmdline" }, `${ps1 ? "PS>" : "$"} ${p.arg}`, h("span", { class: "meta" }, "running…")))),
      rs.length || pend.length ? null : h("div", { class: "faint" }, "Commands run as the agent's service user (root / SYSTEM). Output streams back here."));
    out.scrollTop = out.scrollHeight;
  };
  setTimeout(() => input.focus(), 50);
}

async function sendCmd(id, kind, arg = "", view) {
  try {
    const r = await api(`/api/node/${id}/cmd`, { method: "POST", body: JSON.stringify({ kind, arg }) });
    S.pending.set(r.id, { node: id, kind, arg, view });
    if (kind !== "exec" && !view) toast(`${kind} ${arg.split(" ")[0] || ""} sent to ${title(S.nodes.get(id))}`);
    const n = S.nodes.get(id); if (n) updateDetail(n, true);
  } catch (e) { if (e.message !== "unauthorized") toast("Command failed: " + e.message, true); }
}

function modal(...kids) {
  const m = $("#modal"); m.replaceChildren(...kids); m.hidden = false; return m;
}
function confirmAct(t, msg, fn) {
  const m = modal(h("div", { class: "dialog", style: { width: "min(460px,100%)" } }, h("h2", null, ic("alert"), t), h("p", null, msg),
    h("div", { class: "foot" }, h("button", { class: "btn", onclick: () => (m.hidden = true) }, "Cancel"), h("button", { class: "btn danger", onclick: () => { m.hidden = true; fn(); } }, ic("check"), "Confirm"))));
}
function showOutput(t, text) {
  const m = modal(h("div", { class: "dialog wide" }, h("h2", null, ic("logs"), t), h("pre", { class: "logview" }, text || "(empty)"),
    h("div", { class: "foot" }, h("button", { class: "btn", onclick: () => navigator.clipboard?.writeText(text) }, ic("copy"), "Copy"), h("button", { class: "btn", onclick: () => (m.hidden = true) }, "Close"))));
  const pre = $("pre", m); pre.scrollTop = pre.scrollHeight;
}

// ------------------------------------------------------------------ add machine
async function showAdd() {
  let info;
  try { info = S.info = await api("/api/info"); } catch { return; }
  let manage = true;
  const unix = h("div", { class: "code" }), win = h("div", { class: "code" }), winSrc = h("pre", { class: "logview", style: { maxHeight: "220px" } });
  const copyBtn = (el) => h("button", { class: "btn sm copy", onclick: (e) => { navigator.clipboard?.writeText(el.dataset.v); const b = e.currentTarget; b.replaceChildren(ic("check", "sm"), "Copied"); setTimeout(() => b.replaceChildren(ic("copy", "sm"), "Copy"), 1500); } }, ic("copy", "sm"), "Copy");
  const render = () => {
    unix.dataset.v = manage ? info.cmds.unixManage : info.cmds.unix; win.dataset.v = manage ? info.cmds.winManage : info.cmds.win;
    unix.replaceChildren(unix.dataset.v, copyBtn(unix)); win.replaceChildren(win.dataset.v, copyBtn(win));
    winSrc.textContent = info.cmds.winScript.replace(/ --manage$/m, manage ? " --manage" : "");
  };
  render();
  const want = ["linux-amd64", "linux-arm64", "linux-riscv64", "linux-arm", "darwin-arm64", "darwin-amd64", "windows-amd64.exe", "windows-arm64.exe"];
  const have = new Set(info.dist || []);
  const sec = info.tls === "auto" ? [ic("shield"), h("span", null, "TLS with a self-signed certificate. Installers and agents ", h("b", null, "pin its public key"), ", so no CA is needed and impersonation is blocked."), h("span", { class: "mono faint" }, info.pin)]
    : info.tls === "cert" ? [ic("shield"), "TLS with your certificate."] : [ic("alert"), "TLS is off. Only use this behind an HTTPS reverse proxy or on a trusted network."];
  const m = modal(h("div", { class: "dialog" },
    h("h2", null, ic("plus"), "Add a machine"), h("p", null, "Run one line on the machine. Argus installs as a system service (systemd, OpenRC, launchd or Task Scheduler), enrolls with a per-machine key, and starts streaming within seconds."),
    h("div", { class: "secrow" }, ...sec),
    h("label", { class: "check" }, h("input", { type: "checkbox", checked: true, onchange: (e) => { manage = e.target.checked; render(); } }), ic("sliders"), "Allow remote management (console, containers, kill, reboot, shutdown)"),
    h("div", { class: "field" }, h("label", null, ic("term", "sm"), "macOS · Linux · VPS (amd64, arm64, riscv64, armv7)"), unix),
    h("div", { class: "field" }, h("label", null, OS.windows ? h("span", { html: OS.windows, style: { width: "13px", display: "inline-flex" } }) : null, "Windows (Admin terminal: cmd, PowerShell or Windows Terminal)"), win),
    h("details", null, h("summary", null, "Show the Windows script before running it"), winSrc),
    h("div", { class: "field" }, h("label", null, ic("box", "sm"), "Agent binaries this hub serves"),
      h("div", { class: "chips" }, want.map((w) => h("span", { class: "tag " + (have.has("argus-" + w) ? "ok" : "warn"), title: have.has("argus-" + w) ? "available" : "missing: build with ./build.sh into the hub's dist/" }, ic(have.has("argus-" + w) ? "check" : "alert"), w)))),
    h("div", { class: "foot" },
      h("button", { class: "btn ghost", title: "Old install commands stop working for new machines. Enrolled machines keep their own keys.", onclick: () => confirmAct("Rotate the enroll token?", "Install commands you've already shared stop working. Machines that are already enrolled are unaffected.", async () => { S.info = await api("/api/enroll/rotate", { method: "POST" }); toast("Enroll token rotated"); showAdd(); }) }, ic("key"), "Rotate enroll token"),
      h("button", { class: "btn", onclick: () => (m.hidden = true) }, "Done"))));
}

// ------------------------------------------------------------------ login
function showLogin() {
  if ($("#login")) return;
  closeDetail(); setLive(false); S.es?.close(); S.es = null;
  $("#summary").replaceChildren();
  const inp = h("input", { class: "input", type: "password", placeholder: "admin token", autocomplete: "current-password", "aria-label": "Admin token" });
  const err = h("div", { class: "muted", style: { minHeight: "22px", fontSize: "13px" } });
  $("#main").replaceChildren(h("div", { class: "login", id: "login" }, h("div", { class: "dialog" },
    h("h2", null, ic("lock"), "Sign in to Argus"), h("p", null, "Enter the hub's admin token. The hub prints it on first start and keeps it in admin.token in its data folder (or run: argus token)."),
    h("form", { onsubmit: async (e) => {
      e.preventDefault();
      const r = await fetch("/api/login", { method: "POST", credentials: "same-origin", headers: { "X-Argus": "1", "Content-Type": "application/json" }, body: JSON.stringify({ token: inp.value.trim() }) });
      if (r.ok) location.reload(); else err.textContent = r.status === 429 ? "Too many attempts. Wait a few minutes." : "That token didn't work.";
    } }, h("div", { class: "field" }, inp), err, h("button", { class: "btn primary", style: { width: "100%", height: "42px" } }, ic("eye"), "Sign in")))));
  setTimeout(() => inp.focus(), 30);
}

// ------------------------------------------------------------------ live
function ingestNode(v) {
  const n = S.nodes.get(v.id) || { id: v.id, hist: [], results: [] };
  for (const k of ["static", "last", "seen", "addr", "hist"]) if (v[k]) n[k] = v[k];
  n.local = v.local;
  S.nodes.set(v.id, n);
  return n;
}

let raf = 0; const dirty = new Set();
function schedule(id) {
  dirty.add(id);
  if (raf) return;
  raf = requestAnimationFrame(() => {
    raf = 0;
    if (document.hidden) { dirty.clear(); return; }
    for (const id of dirty) {
      const n = S.nodes.get(id); if (!n) continue;
      if (S.cards.has(id)) updateCard(n);
      if (S.detail?.id === id) updateDetail(n);
    }
    dirty.clear();
    renderSummary();
  });
}

function setLive(on) {
  S.live = on;
  const el = $("#live"); el.classList.toggle("off", !on);
  el.lastChild.textContent = on ? "Live" : "Reconnecting";
}

function setFocus(id) {
  if (S.focus === id && S.es) return;
  S.focus = id; connect();
}

function connect() {
  S.es?.close();
  const es = (S.es = new EventSource("/api/stream" + (S.focus ? "?focus=" + encodeURIComponent(S.focus) : "")));
  const clock = (e) => { S.skew = JSON.parse(e.data).now - Date.now(); setLive(true); };
  es.addEventListener("hello", clock); es.addEventListener("ping", clock);
  es.addEventListener("m", (e) => {
    const v = JSON.parse(e.data), n = S.nodes.get(v.id);
    if (!n) { load(); return; }
    n.last = v.m; n.seen = v.seen; n.iv = v.iv;
    n.hist.push(v.p); if (n.hist.length > HIST_MAX) n.hist.splice(0, n.hist.length - HIST_MAX);
    schedule(v.id);
  });
  es.addEventListener("node", (e) => {
    const v = JSON.parse(e.data), isNew = !S.nodes.has(v.id);
    ingestNode({ id: v.id, static: v.static, local: v.local, seen: now() });
    if (isNew && !S.detail) renderFleet(); else schedule(v.id);
  });
  es.addEventListener("result", (e) => {
    const v = JSON.parse(e.data), n = S.nodes.get(v.id), r = v.result, p = S.pending.get(r.id);
    S.pending.delete(r.id);
    if (n) { n.results = (n.results || []).concat(r).slice(-100); if (S.detail?.id === v.id) updateDetail(n, true); }
    if (p?.view) showOutput(p.view, r.output);
    else if (r.kind !== "exec" && p) toast(`${title(n || { id: v.id })}: ${r.output}`, !r.ok);
  });
  es.addEventListener("gone", (e) => { S.nodes.delete(JSON.parse(e.data).id); if (!S.detail) renderFleet(); });
  es.onerror = () => { setLive(false); if (es.readyState === 2) setTimeout(() => S.es === es && connect(), 3000); };
}

async function load() {
  const r = await api("/api/nodes");
  S.skew = r.now - Date.now();
  const seen = new Set();
  for (const v of r.nodes) { ingestNode(v); seen.add(v.id); }
  for (const id of S.nodes.keys()) if (!seen.has(id)) S.nodes.delete(id);
  route();
}

function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/");
  if (parts[0] === "n" && parts[1]) renderDetail(parts[1], TABS.some(([t]) => t === parts[2]) ? parts[2] : "overview");
  else { renderFleet(); setFocus(""); }
  renderSummary();
  scrollTo({ top: 0 });
}

// Status ticker: offline detection and relative times keep moving with no events.
setInterval(() => {
  for (const id of S.nodes.keys()) schedule(id);
  if (!S.detail && S.view === "list" && !document.hidden && !$("#login")) renderList(visibleNodes());
}, 3000);

addEventListener("hashchange", route);
document.addEventListener("visibilitychange", () => { if (!document.hidden) for (const id of S.nodes.keys()) schedule(id); });
$("#q").addEventListener("input", (e) => { S.q = e.target.value; if (!S.detail) renderFleet(); });
addEventListener("keydown", (e) => {
  if (e.key === "/" && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) { e.preventDefault(); $("#q").focus(); }
  if (e.key === "Escape") $("#modal").hidden = true;
});
const setViewIcon = () => $("#viewToggle").replaceChildren(ic(S.view === "grid" ? "list" : "grid"));
$("#viewToggle").addEventListener("click", () => { S.view = S.view === "grid" ? "list" : "grid"; lsSet("argus.view", S.view); setViewIcon(); if (!S.detail) renderFleet(); });
setViewIcon();
$("#searchIc").replaceWith(ic("search"));
$("#addIc").replaceWith(ic("plus"));
$("#logoutBtn").append(ic("logout"));
$("#logoutBtn").addEventListener("click", async () => { await fetch("/api/logout", { method: "POST", headers: { "X-Argus": "1" } }); location.reload(); });
$("#addBtn").addEventListener("click", showAdd);
$("#modal").addEventListener("click", (e) => { if (e.target.id === "modal") e.target.hidden = true; });

load().catch(() => {});
