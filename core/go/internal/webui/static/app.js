// AURA Workspace Web App (Blueprint 13A.4, ADR-0041). A client of the public API.
// Rule: every piece of data is rendered with textContent / attributes, never as HTML.
"use strict";

const I18N = {
  id: {
    loading: "Memuat…", "nav.tasks": "Tugas", "nav.approvals": "Persetujuan", "nav.notifications": "Notifikasi",
    "nav.tools": "Alat L2", "nav.self": "Tentang AURA", logout: "Keluar",
    "login.title": "Masuk ke AURA", "login.password": "Password", "login.code": "Kode dari aplikasi authenticator",
    "login.submit": "Masuk", "login.notsetup": "Akun pemilik belum disiapkan. Minta tautan setup (aura web setup-link).",
    "setup.title": "Siapkan akun pemilik", "setup.password": "Password baru (minimal 12 karakter)", "setup.password2": "Ulangi password",
    "setup.next": "Lanjut", "setup.scan": "Tambahkan akun ini di aplikasi authenticator (Google Authenticator, Aegis, 1Password…) lalu masukkan kode 6 digit.",
    "setup.secret": "Kunci (jika tidak bisa memindai):", "setup.uri": "Buka di aplikasi authenticator", "setup.confirm": "Aktifkan", "setup.done": "Akun siap. Silakan masuk.",
    "setup.mismatch": "Password tidak sama.",
    "tasks.title": "Tugas", "tasks.new": "Tugas baru", "tasks.objective": "Apa yang harus dikerjakan AURA?",
    "tasks.tools": "Alat (pisahkan dengan koma)", "tasks.domains": "Domain yang boleh dibuka (pisahkan dengan koma)",
    "tasks.public": "Konten publik (boleh memakai model gratis yang mungkin mencatat prompt)", "tasks.profile": "Profil login browser (opsional)",
    "tasks.create": "Buat tugas", "tasks.empty": "Belum ada tugas.",
    "ask.title": "Tanya AURA", "ask.placeholder": "Tulis permintaan Tuan…", "ask.send": "Kirim", "ask.options": "Opsi lanjutan", "ask.working": "AURA sedang mengerjakan…",
    "reply.title": "Jawaban AURA", "form.send": "Kirim ke AURA", "reply.files": "File yang ditulis", "reply.details": "Rincian teknis",
    "note.task_failed": "Tugas ini tidak selesai; isi balasan ini belum diverifikasi. Alasan:",
    "note.model_knowledge": "Dijawab dari pengetahuan model, tanpa alat — belum diperiksa terhadap sumber.",
    "note.core_summary": "Model penulis jawaban tidak tersedia; ini ringkasan apa adanya dari Core.",
    "note.ungrounded_numbers": "Angka berikut tidak ditemukan di bukti tugas — periksa dulu:",
    "note.view_trimmed": "Sebagian tampilan dibuang oleh Core karena tidak aman atau tidak sah.", "tasks.cost": "biaya",
    "task.back": "← Semua tugas", "task.objective": "Tujuan", "task.state": "Status", "task.reason": "Alasan", "task.cost": "Biaya",
    "task.question": "AURA bertanya", "task.answer": "Jawaban", "task.send": "Kirim", "task.correct": "Koreksi / arahan tambahan",
    "task.correctHint": "Berlaku untuk tugas ini dan subagennya.", "task.cancel": "Batalkan", "task.pause": "Jeda", "task.resume": "Lanjutkan",
    "task.confirmCancel": "Batalkan tugas ini beserta subagennya?", "task.events": "Aktivitas", "task.files": "File",
    "task.mfa": "Situs meminta kode sekali pakai", "task.mfaHint": "Kode langsung diketik Core ke halaman; tidak disimpan dan tidak dilihat model.",
    "task.mfaSend": "Masukkan kode", "task.approvalPending": "Menunggu persetujuan Anda", "task.open": "Buka",
    "ap.title": "Persetujuan", "ap.empty": "Tidak ada persetujuan yang menunggu.", "ap.action": "Aksi", "ap.target": "Target",
    "ap.side": "Efek samping", "ap.tier": "Tingkat risiko", "ap.cost": "Perkiraan biaya", "ap.task": "Tugas", "ap.step": "Langkah",
    "ap.why": "Alasan", "ap.params": "Parameter persis", "ap.hash": "Hash aksi", "ap.expires": "Kedaluwarsa",
    "ap.code": "Kode authenticator (wajib untuk menyetujui)", "ap.approve": "Setujui aksi persis ini", "ap.reject": "Tolak",
    "ap.approved": "Disetujui.", "ap.rejected": "Ditolak.",
    "no.title": "Notifikasi", "no.empty": "Tidak ada notifikasi.", "no.read": "Tandai dibaca",
    "tools.title": "Alat L2 buatan AURA", "tools.empty": "Belum ada alat.", "tools.source": "Kode sumber", "tools.tests": "Test",
    "tools.report": "Laporan sandbox", "tools.approve": "Setujui kode persis ini", "tools.reject": "Tolak", "tools.disable": "Nonaktifkan",
    "self.title": "Tentang AURA", "err": "Gagal: ", "saved": "Tersimpan.",
    "nav.browser": "Browser", "br.title": "Browser", "br.profiles": "Profil login", "br.sessions": "Sesi terbuka",
    "br.noProfiles": "Belum ada profil login (browser.logins di config).", "br.noSessions": "Tidak ada sesi browser yang terbuka.",
    "br.saved": "sesi tersimpan", "br.start": "Login sendiri", "br.startUrl": "Halaman awal (URL login situs)",
    "br.extra": "Domain tambahan untuk proses login (mis. accounts.google.com, *.gstatic.com)", "br.view": "Lihat",
    "br.takeover": "Ambil alih browser", "br.screenHint": "Ketuk gambar untuk mengklik. AURA berhenti bertindak selama Anda memegang kontrol.",
    "br.type": "Ketik teks (tidak dicatat)", "br.send": "Ketik", "br.refresh": "Segarkan", "br.back": "Kembali", "br.up": "Gulir atas",
    "br.down": "Gulir bawah", "br.return": "Kembalikan kontrol ke AURA", "br.saveClose": "Simpan sesi login & tutup",
    "br.closeOnly": "Tutup tanpa menyimpan", "br.goto": "Buka URL", "br.returned": "Kontrol dikembalikan; AURA membaca ulang halaman.",
  },
  en: {
    loading: "Loading…", "nav.tasks": "Tasks", "nav.approvals": "Approvals", "nav.notifications": "Notifications",
    "nav.tools": "L2 tools", "nav.self": "About AURA", logout: "Sign out",
    "login.title": "Sign in to AURA", "login.password": "Password", "login.code": "Code from your authenticator app",
    "login.submit": "Sign in", "login.notsetup": "The owner account is not set up yet. Ask for a setup link (aura web setup-link).",
    "setup.title": "Set up the owner account", "setup.password": "New password (at least 12 characters)", "setup.password2": "Repeat the password",
    "setup.next": "Next", "setup.scan": "Add this account in your authenticator app (Google Authenticator, Aegis, 1Password…) and enter the 6-digit code.",
    "setup.secret": "Key (if you cannot scan):", "setup.uri": "Open in authenticator app", "setup.confirm": "Activate", "setup.done": "Account ready. Please sign in.",
    "setup.mismatch": "Passwords do not match.",
    "tasks.title": "Tasks", "tasks.new": "New task", "tasks.objective": "What should AURA do?",
    "tasks.tools": "Tools (comma-separated)", "tasks.domains": "Domains it may open (comma-separated)",
    "tasks.public": "Public content (free models that may log prompts are allowed)", "tasks.profile": "Browser login profile (optional)",
    "tasks.create": "Create task", "tasks.empty": "No tasks yet.",
    "ask.title": "Ask AURA", "ask.placeholder": "Write your request…", "ask.send": "Send", "ask.options": "Advanced options", "ask.working": "AURA is working…",
    "reply.title": "AURA's reply", "form.send": "Send to AURA", "reply.files": "Files written", "reply.details": "Technical details",
    "note.task_failed": "This task was not completed; nothing in this reply is verified. Reason:",
    "note.model_knowledge": "Answered from the model's own knowledge, without tools — not checked against a source.",
    "note.core_summary": "No model could write the reply; this is a plain summary from the Core.",
    "note.ungrounded_numbers": "These figures were not found in the task's evidence — check them first:",
    "note.view_trimmed": "Part of the view was removed by the Core because it was unsafe or malformed.", "tasks.cost": "cost",
    "task.back": "← All tasks", "task.objective": "Objective", "task.state": "State", "task.reason": "Reason", "task.cost": "Cost",
    "task.question": "AURA asks", "task.answer": "Answer", "task.send": "Send", "task.correct": "Correction / extra guidance",
    "task.correctHint": "Applies to this task and its subagents.", "task.cancel": "Cancel", "task.pause": "Pause", "task.resume": "Resume",
    "task.confirmCancel": "Cancel this task and its subagents?", "task.events": "Activity", "task.files": "Files",
    "task.mfa": "The site asks for a one-time code", "task.mfaHint": "The Core types the code straight into the page; it is not stored or seen by a model.",
    "task.mfaSend": "Enter code", "task.approvalPending": "Waiting for your approval", "task.open": "Open",
    "ap.title": "Approvals", "ap.empty": "No approvals waiting.", "ap.action": "Action", "ap.target": "Target",
    "ap.side": "Side effect", "ap.tier": "Risk tier", "ap.cost": "Estimated cost", "ap.task": "Task", "ap.step": "Step",
    "ap.why": "Why", "ap.params": "Exact parameters", "ap.hash": "Action hash", "ap.expires": "Expires",
    "ap.code": "Authenticator code (required to approve)", "ap.approve": "Approve exactly this action", "ap.reject": "Reject",
    "ap.approved": "Approved.", "ap.rejected": "Rejected.",
    "no.title": "Notifications", "no.empty": "No notifications.", "no.read": "Mark read",
    "tools.title": "AURA-built L2 tools", "tools.empty": "No tools yet.", "tools.source": "Source", "tools.tests": "Tests",
    "tools.report": "Sandbox report", "tools.approve": "Approve exactly this source", "tools.reject": "Reject", "tools.disable": "Disable",
    "self.title": "About AURA", "err": "Failed: ", "saved": "Saved.",
    "nav.browser": "Browser", "br.title": "Browser", "br.profiles": "Login profiles", "br.sessions": "Open sessions",
    "br.noProfiles": "No login profiles yet (browser.logins in the config).", "br.noSessions": "No open browser sessions.",
    "br.saved": "session saved", "br.start": "Sign in myself", "br.startUrl": "Start page (the site's login URL)",
    "br.extra": "Extra domains the sign-in needs (e.g. accounts.google.com, *.gstatic.com)", "br.view": "View",
    "br.takeover": "Take over the browser", "br.screenHint": "Tap the picture to click. AURA stops acting while you have control.",
    "br.type": "Type text (not recorded)", "br.send": "Type", "br.refresh": "Refresh", "br.back": "Back", "br.up": "Scroll up",
    "br.down": "Scroll down", "br.return": "Return control to AURA", "br.saveClose": "Save the login session & close",
    "br.closeOnly": "Close without saving", "br.goto": "Open URL", "br.returned": "Control returned; AURA reads the page again.",
  },
};

let lang = "id";
try { lang = localStorage.getItem("aura.lang") || (navigator.language || "id").slice(0, 2); } catch (e) { /* storage unavailable */ }
if (!I18N[lang]) lang = "id";
const t = (k) => (I18N[lang] && I18N[lang][k]) || I18N.en[k] || k;
let csrf = "";
let timer = null;

// ---------------------------------------------------------------- DOM helpers (text only)
function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "text") n.textContent = String(v);
    else if (k === "on") for (const [ev, fn] of Object.entries(v)) n.addEventListener(ev, fn);
    else n.setAttribute(k, String(v));
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    n.append(typeof c === "string" || typeof c === "number" ? document.createTextNode(String(c)) : c);
  }
  return n;
}
function render(...nodes) {
  const m = document.getElementById("main");
  m.replaceChildren(...nodes);
}
function toast(msg) {
  const b = document.getElementById("toast");
  b.textContent = msg;
  b.style.display = "block";
  setTimeout(() => { b.style.display = "none"; }, 4000);
}
function applyI18n() {
  document.documentElement.lang = lang;
  for (const n of document.querySelectorAll("[data-i18n]")) n.textContent = t(n.dataset.i18n);
  document.getElementById("lang").textContent = lang === "id" ? "EN" : "ID";
}
function fmtTime(v) {
  if (!v) return "";
  const d = typeof v === "number" ? new Date(v) : new Date(String(v));
  return isNaN(d) ? String(v) : d.toLocaleString(lang === "id" ? "id-ID" : "en-GB");
}
function stateBadge(s) { return el("span", { class: "state s-" + String(s).replace(/[^A-Z_]/g, ""), text: s }); }
function pretty(v) { return el("pre", { text: JSON.stringify(v, null, 2) }); }

// ---------------------------------------------------------------- API
async function api(method, path, body) {
  const opts = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  if (method !== "GET" && csrf) opts.headers["X-CSRF-Token"] = csrf;
  const r = await fetch("/api/v1" + path, opts);
  let data = null;
  const text = await r.text();
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
  if (!r.ok) {
    const msg = (data && data.error && (data.error.message || data.error.code)) || r.status;
    const err = new Error(String(msg));
    err.status = r.status;
    err.code = data && data.error && data.error.code;
    throw err;
  }
  return data;
}
async function guard(fn) {
  try { return await fn(); } catch (e) {
    if (e.status === 401 && !String(location.hash).startsWith("#/setup")) { csrf = ""; showNav(false); routeLogin(); return; }
    toast(t("err") + e.message);
  }
}
function showNav(on) {
  document.getElementById("nav").hidden = !on;
  document.getElementById("logout").hidden = !on;
}

// ---------------------------------------------------------------- login & setup
function routeLogin(info) {
  const pw = el("input", { type: "password", id: "pw", autocomplete: "current-password", required: true });
  const code = el("input", { type: "text", id: "code", inputmode: "numeric", autocomplete: "one-time-code", pattern: "[0-9]{6}", maxlength: 6, required: true });
  const form = el("form", { class: "card", on: { submit: async (ev) => {
    ev.preventDefault();
    try {
      const r = await api("POST", "/web/login", { password: pw.value, code: code.value });
      csrf = r.csrf; pw.value = ""; showNav(true); location.hash = "#/"; route();
    } catch (e) { toast(t("err") + e.message); code.value = ""; }
  } } },
    el("label", { for: "pw", text: t("login.password") }), pw,
    el("label", { for: "code", text: t("login.code") }), code,
    el("p", {}, el("button", { class: "primary", type: "submit", text: t("login.submit") })));
  render(el("h1", { text: t("login.title") }), info && info.set_up === false ? el("p", { class: "card", text: t("login.notsetup") }) : null, form);
}

function routeSetup(token) {
  const pw = el("input", { type: "password", id: "pw", autocomplete: "new-password", minlength: 12, required: true });
  const pw2 = el("input", { type: "password", id: "pw2", autocomplete: "new-password", minlength: 12, required: true });
  const form = el("form", { class: "card", on: { submit: async (ev) => {
    ev.preventDefault();
    if (pw.value !== pw2.value) { toast(t("setup.mismatch")); return; }
    try {
      const r = await api("POST", "/web/setup/start", { token, password: pw.value });
      pw.value = ""; pw2.value = "";
      setupStep2(token, r);
    } catch (e) { toast(t("err") + e.message); }
  } } },
    el("label", { for: "pw", text: t("setup.password") }), pw,
    el("label", { for: "pw2", text: t("setup.password2") }), pw2,
    el("p", {}, el("button", { class: "primary", type: "submit", text: t("setup.next") })));
  render(el("h1", { text: t("setup.title") }), form);
}

function setupStep2(token, r) {
  const code = el("input", { type: "text", id: "code", inputmode: "numeric", autocomplete: "one-time-code", maxlength: 6, required: true });
  const form = el("form", { class: "card", on: { submit: async (ev) => {
    ev.preventDefault();
    try {
      await api("POST", "/web/setup/confirm", { token, code: code.value });
      toast(t("setup.done")); history.replaceState(null, "", "/app/#/login"); routeLogin({ set_up: true });
    } catch (e) { toast(t("err") + e.message); }
  } } },
    el("p", { text: t("setup.scan") }),
    el("p", {}, el("a", { href: r.otpauth_uri, text: t("setup.uri") })),
    el("p", { class: "muted", text: t("setup.secret") }), el("pre", { text: r.secret }),
    el("label", { for: "code", text: t("login.code") }), code,
    el("p", {}, el("button", { class: "primary", type: "submit", text: t("setup.confirm") })));
  render(el("h1", { text: t("setup.title") }), form);
}

// ---------------------------------------------------------------- owner reply + adaptive view (ADR-0055)
// A view is declarative data sanitised by the Core. Every block is drawn with text nodes and SVG attributes; nothing in it is HTML.
const SVG = "http://www.w3.org/2000/svg";
function svg(tag, attrs, ...children) {
  const n = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, String(v));
  for (const c of children) if (c) n.append(typeof c === "string" ? document.createTextNode(c) : c);
  return n;
}
const PALETTE = ["#5aa9ff", "#3ecf8e", "#ffb454", "#ff6b6b", "#b48cff", "#4dd0e1", "#f48fb1", "#c0ca33"];
function fmtNum(n) { return Number(n).toLocaleString(lang === "id" ? "id-ID" : "en-GB", { maximumFractionDigits: 2 }); }
function chartBlock(b) {
  const pts = (b.items || []).filter((it) => typeof it.number === "number").slice(0, 30);
  if (pts.length < 2) return null;
  const W = 640, H = 260, L = 8, R = 8, T = 14, B = 46;
  const root = svg("svg", { viewBox: "0 0 " + W + " " + H, class: "chart", role: "img", "aria-label": b.title || "chart" });
  if (b.chart === "pie") {
    const total = pts.reduce((a, p) => a + Math.max(p.number, 0), 0);
    if (total <= 0) return null;
    let a0 = -Math.PI / 2;
    const cx = 130, cy = H / 2, r = 105;
    pts.forEach((p, i) => {
      const frac = Math.max(p.number, 0) / total, a1 = a0 + frac * 2 * Math.PI;
      const x0 = cx + r * Math.cos(a0), y0 = cy + r * Math.sin(a0), x1 = cx + r * Math.cos(a1), y1 = cy + r * Math.sin(a1);
      const d = frac >= 0.9999 ? "M " + (cx - r) + " " + cy + " a " + r + " " + r + " 0 1 0 " + 2 * r + " 0 a " + r + " " + r + " 0 1 0 " + -2 * r + " 0"
        : "M " + cx + " " + cy + " L " + x0 + " " + y0 + " A " + r + " " + r + " 0 " + (frac > 0.5 ? 1 : 0) + " 1 " + x1 + " " + y1 + " Z";
      root.append(svg("path", { d, fill: PALETTE[i % PALETTE.length] }));
      if (i < 10) {
        root.append(svg("rect", { x: 270, y: 20 + i * 23, width: 12, height: 12, fill: PALETTE[i % PALETTE.length] }));
        root.append(svg("text", { x: 290, y: 31 + i * 23, class: "ct" }, p.label.slice(0, 34) + " — " + fmtNum(p.number) + " (" + Math.round(frac * 100) + "%)"));
      }
      a0 = a1;
    });
    return root;
  }
  const max = Math.max(...pts.map((p) => p.number), 0), min = Math.min(...pts.map((p) => p.number), 0);
  const span = max - min || 1, pw = (W - L - R) / pts.length;
  const y = (v) => T + (H - T - B) * (1 - (v - min) / span);
  root.append(svg("line", { x1: L, x2: W - R, y1: y(0), y2: y(0), class: "axis" }));
  const line = [];
  pts.forEach((p, i) => {
    const cx = L + pw * i + pw / 2;
    if (b.chart === "line") {
      line.push(cx + "," + y(p.number));
      root.append(svg("circle", { cx, cy: y(p.number), r: 3.5, fill: PALETTE[0] }));
    } else {
      const top = Math.min(y(p.number), y(0)), h = Math.max(Math.abs(y(p.number) - y(0)), 1);
      root.append(svg("rect", { x: cx - pw * 0.35, y: top, width: pw * 0.7, height: h, rx: 3, fill: PALETTE[0] }));
    }
    if (pts.length <= 14) {
      root.append(svg("text", { x: cx, y: Math.max(y(p.number) - 5, 11), class: "ct mid" }, fmtNum(p.number)));
      root.append(svg("text", { x: cx, y: H - B + 16, class: "ct mid" }, p.label.slice(0, Math.max(4, Math.floor(pw / 7)))));
    }
  });
  if (b.chart === "line") root.insertBefore(svg("polyline", { points: line.join(" "), fill: "none", stroke: PALETTE[0], "stroke-width": 2.5 }), root.children[1]);
  const wrap = el("div", {}, root);
  if (pts.length > 14) wrap.append(el("p", { class: "muted", text: pts[0].label + " … " + pts[pts.length - 1].label }));
  return wrap;
}
// formBlock asks for the fields of a proposed request; submitting hands the
// filled request to onAction like an action button — nothing is sent by itself.
function formBlock(b, onAction) {
  const inputs = (b.items || []).map((it) => {
    const opts = String(it.value || "").split("|").map((o) => o.trim()).filter(Boolean);
    const input = opts.length > 1
      ? el("select", { required: true }, el("option", { value: "", text: "—" }), opts.map((o) => el("option", { value: o, text: o })))
      : el("input", { type: "text", required: true, maxlength: 300, placeholder: it.note || "" });
    return { name: it.label, input, row: el("label", {}, it.label, input, opts.length > 1 && it.note ? el("div", { class: "muted", text: it.note }) : null) };
  });
  return el("form", { class: "vform", on: { submit: (e) => {
    e.preventDefault();
    let req = b.text;
    for (const f of inputs) req = req.split("{" + f.name + "}").join(f.input.value.trim());
    onAction(req);
  } } }, inputs.map((f) => f.row), el("button", { type: "submit", text: t("form.send") }));
}
function viewBlock(b, onAction) {
  const items = b.items || [];
  let body = null;
  switch (b.type) {
    case "text": body = el("p", { class: "pre", text: b.text }); break;
    case "code": body = el("pre", { text: b.text }); break;
    case "callout": body = el("div", { class: "callout tone-" + (["info", "success", "warning", "danger"].includes(b.tone) ? b.tone : "info"), text: b.text }); break;
    case "metrics": body = el("div", { class: "metrics" }, items.map((it) => el("div", { class: "metric" },
      el("div", { class: "muted", text: it.label }), el("div", { class: "big", text: it.value }), it.note ? el("div", { class: "muted", text: it.note }) : null))); break;
    case "kv": body = el("dl", {}, items.map((it) => [el("dt", { text: it.label }), el("dd", { text: it.value + (it.note ? " — " + it.note : "") })])); break;
    case "table": body = el("div", { class: "scroll" }, el("table", {},
      el("thead", {}, el("tr", {}, (b.columns || []).map((c) => el("th", { text: c })))),
      el("tbody", {}, (b.rows || []).map((r) => el("tr", {}, r.map((c) => el("td", { text: c }))))))); break;
    case "list": body = el("ul", {}, items.map((it) => el("li", {}, it.label, it.value ? ": " + it.value : "", it.note ? el("div", { class: "muted", text: it.note }) : null))); break;
    case "steps": body = el("ol", {}, items.map((it) => el("li", {}, it.label, it.note ? el("div", { class: "muted", text: it.note }) : null))); break;
    case "chart": body = chartBlock(b); break;
    case "links": body = el("ul", {}, items.filter((it) => /^https?:\/\//.test(it.url || "")).map((it) => el("li", {},
      el("a", { href: it.url, target: "_blank", rel: "noopener noreferrer", text: it.label || it.url })))); break;
    case "hero": body = el("div", { class: "hero tone-" + (["info", "success", "warning", "danger"].includes(b.tone) ? b.tone : "info") },
      el("div", { class: "big", text: b.title }), b.text ? el("p", { class: "pre", text: b.text }) : null); break;
    case "quote": body = el("blockquote", { class: "pre", text: b.text }); break;
    case "timeline": body = el("ol", { class: "timeline" }, items.map((it) => el("li", {},
      el("span", { class: "muted", text: it.label }), " ", it.value, it.note ? el("div", { class: "muted", text: it.note }) : null))); break;
    case "progress": body = el("div", {}, items.map((it) => { const n = Math.max(0, Math.min(100, Number(it.number) || 0)); return el("div", { class: "prog" },
      el("div", { class: "row between" }, el("span", { text: it.label }), el("span", { class: "muted", text: Math.round(n) + "%" })),
      el("progress", { max: 100, value: n }), it.note ? el("div", { class: "muted", text: it.note }) : null); })); break;
    case "compare": body = el("div", { class: "scroll" }, el("table", {},
      el("thead", {}, el("tr", {}, el("th", {}), (b.columns || []).map((c) => el("th", { class: c === b.text ? "pick" : "", text: c === b.text ? c + " ★" : c })))),
      el("tbody", {}, (b.rows || []).map((r) => el("tr", {}, r.map((c, i) => el(i ? "td" : "th", { class: i && (b.columns || [])[i - 1] === b.text ? "pick" : "", text: c }))))))); break;
    case "form": body = onAction ? formBlock(b, onAction) : null; break;
    case "actions": body = onAction ? el("div", { class: "row" }, items.map((it) => el("button", { type: "button", title: it.value,
      on: { click: () => onAction(it.value) }, text: it.label }))) : null; break;
  }
  if (!body) return null;
  return el("section", { class: "vb" + (b.emphasis === "strong" ? " strong" : b.emphasis === "quiet" ? " quiet" : "") },
    b.title && b.type !== "hero" ? el("h3", { text: b.title }) : null, body);
}
function replyCard(tk, onAction) {
  const r = tk.reply;
  if (!r) return null;
  const notes = (r.notes || []).map((n) => el("p", { class: "note", text: "ⓘ " + t("note." + n.code) + ((n.code === "ungrounded_numbers" || n.code === "task_failed") && n.detail ? " " + n.detail : "") }));
  const files = (r.files || []).length ? el("p", { class: "muted" }, t("reply.files") + ": ", (r.files || []).map((f, i) => [i ? ", " : "",
    el("a", { href: "/api/v1/tasks/" + encodeURIComponent(tk.id) + "/files/" + String(f).split("/").map(encodeURIComponent).join("/"), target: "_blank", rel: "noopener noreferrer", text: f })])) : null;
  return el("div", { class: "card reply" }, el("p", { class: "pre lead", text: r.text }),
    (r.view || []).map((b) => viewBlock(b, onAction)), files, notes);
}
async function ask(objective, pub, extra) {
  const body = Object.assign({ objective, data_class: pub ? "public" : "private" }, extra || {});
  const r = await guard(() => api("POST", "/tasks", body));
  if (r) location.hash = "#/task/" + encodeURIComponent(r.id);
}

// ---------------------------------------------------------------- tasks
async function routeTasks() {
  const data = await guard(() => api("GET", "/tasks?limit=50"));
  if (!data) return;
  const objective = el("textarea", { id: "obj", required: true, maxlength: 8000 });
  const tools = el("input", { type: "text", id: "tools", value: "web.search, web.fetch, workspace.write" });
  const domains = el("input", { type: "text", id: "domains", placeholder: "example.com, *.wikipedia.org" });
  const profile = el("input", { type: "text", id: "profile" });
  const pub = el("input", { type: "checkbox", id: "pub" });
  const split = (v) => v.split(",").map((x) => x.trim()).filter(Boolean);
  objective.setAttribute("placeholder", t("ask.placeholder"));
  try { pub.checked = localStorage.getItem("aura.public") === "1"; } catch (e) { /* storage may be blocked */ }
  const form = el("form", { class: "card", on: { submit: async (ev) => {
    ev.preventDefault();
    try { localStorage.setItem("aura.public", pub.checked ? "1" : "0"); } catch (e) { /* ignore */ }
    const extra = { tools: split(tools.value), allow_domains: split(domains.value) };
    if (profile.value.trim()) extra.browser_profile = profile.value.trim();
    await ask(objective.value, pub.checked, extra);
  } } },
    el("label", { for: "obj", text: t("ask.title") }), objective,
    el("label", {}, pub, " ", t("tasks.public")),
    el("details", {}, el("summary", { text: t("ask.options") }),
      el("label", { for: "tools", text: t("tasks.tools") }), tools,
      el("label", { for: "domains", text: t("tasks.domains") }), domains,
      el("label", { for: "profile", text: t("tasks.profile") }), profile),
    el("p", {}, el("button", { class: "primary", type: "submit", text: t("ask.send") })));
  const list = (data.tasks || []).map((tk) => el("div", { class: "card" },
    el("a", { class: "task", href: "#/task/" + encodeURIComponent(tk.id) },
      el("div", { class: "row" }, stateBadge(tk.state), el("span", { class: "muted", text: fmtTime(tk.created_at) }),
        el("span", { class: "muted", text: t("tasks.cost") + " $" + Number(tk.cost_usd || 0).toFixed(4) })),
      el("div", { class: "obj", text: tk.objective }),
      tk.reply ? el("div", { class: "preview", text: String(tk.reply.text).slice(0, 280) }) : null)));
  render(el("h1", { text: t("tasks.title") }), form,
    list.length ? list : el("p", { class: "muted", text: t("tasks.empty") }));
}

async function routeTask(id) {
  const tl = await guard(() => api("GET", "/tasks/" + encodeURIComponent(id) + "/timeline"));
  if (!tl) return;
  const tk = tl.task;
  const full = await guard(() => api("GET", "/tasks/" + encodeURIComponent(id)));
  const evs = await guard(() => api("GET", "/tasks/" + encodeURIComponent(id) + "/events"));
  const files = await guard(() => api("GET", "/tasks/" + encodeURIComponent(id) + "/files"));
  const aps = (await guard(() => api("GET", "/approvals"))) || { approvals: [] };
  const mine = (aps.approvals || []).filter((a) => a.task_id === id);
  const parts = [el("p", {}, el("a", { href: "#/", text: t("task.back") })), el("h1", {}, stateBadge(tk.state))];
  parts.push(el("div", { class: "card" }, el("dl", {},
    el("dt", { text: t("task.objective") }), el("dd", { text: tk.objective }),
    tk.reason ? [el("dt", { text: t("task.reason") }), el("dd", { text: (tk.reason_code ? tk.reason_code + " — " : "") + tk.reason })] : null,
    el("dt", { text: t("task.cost") }), el("dd", { text: "$" + Number(tk.cost_usd || 0).toFixed(4) }))));
  const terminalNow = ["COMPLETED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED"].includes(tk.state);
  const card = full && replyCard(full, (text) => ask(text, full.data_class === "public"));
  if (card) parts.push(el("h2", { text: t("reply.title") }), card);
  else if (!terminalNow) parts.push(el("p", { class: "muted", text: t("ask.working") }));
  for (const a of mine) parts.push(el("div", { class: "card" }, el("strong", { text: t("task.approvalPending") + ": " + a.tool_id }), " ",
    el("a", { href: "#/approvals", text: t("task.open") })));
  const sess = (await guard(() => api("GET", "/browser/sessions"))) || { sessions: [] };
  if ((sess.sessions || []).some((x) => x.key === id)) {
    parts.push(el("div", { class: "row" }, el("button", { type: "button", on: { click: async () => {
      if (await guard(() => api("POST", "/tasks/" + encodeURIComponent(id) + "/browser/takeover"))) location.hash = "#/screen/" + encodeURIComponent(id);
    } }, text: t("br.takeover") })));
  }
  // A pending question.
  if (full && full.question) {
    const ans = el("textarea", { id: "ans", required: true, maxlength: 4000 });
    parts.push(el("form", { class: "card", on: { submit: async (ev) => {
      ev.preventDefault();
      if (await guard(() => api("POST", "/tasks/" + encodeURIComponent(id) + "/input", { answer: ans.value }))) { toast(t("saved")); route(); }
    } } }, el("strong", { text: t("task.question") }), el("p", { text: full.question }),
      el("label", { for: "ans", text: t("task.answer") }), ans, el("p", {}, el("button", { class: "primary", type: "submit", text: t("task.send") }))));
  }
  // A one-time code for a browser login.
  if (tk.state === "WAITING_EXTERNAL" && String(tk.wait_reason || "").startsWith("event:mfa:")) {
    const code = el("input", { type: "text", id: "mfa", inputmode: "numeric", autocomplete: "off", maxlength: 64, required: true });
    parts.push(el("form", { class: "card", on: { submit: async (ev) => {
      ev.preventDefault();
      const v = code.value; code.value = "";
      if (await guard(() => api("POST", "/tasks/" + encodeURIComponent(id) + "/browser-code", { code: v }))) { toast(t("saved")); route(); }
    } } }, el("strong", { text: t("task.mfa") }), el("p", { class: "muted", text: t("task.mfaHint") }), code,
      el("p", {}, el("button", { class: "primary", type: "submit", text: t("task.mfaSend") }))));
  }
  // Controls.
  const act = (a) => async () => {
    if (a === "cancel" && !confirm(t("task.confirmCancel"))) return;
    if (await guard(() => api("POST", "/tasks/" + encodeURIComponent(id) + "/" + a))) route();
  };
  const terminal = ["COMPLETED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED"].includes(tk.state);
  if (!terminal) parts.push(el("div", { class: "row" },
    tk.state === "PAUSED" ? el("button", { type: "button", on: { click: act("resume") }, text: t("task.resume") })
      : el("button", { type: "button", on: { click: act("pause") }, text: t("task.pause") }),
    el("button", { type: "button", class: "danger", on: { click: act("cancel") }, text: t("task.cancel") })));
  // Correction.
  if (!terminal) {
    const c = el("textarea", { id: "corr", maxlength: 4000, required: true });
    parts.push(el("form", { class: "card", on: { submit: async (ev) => {
      ev.preventDefault();
      if (await guard(() => api("POST", "/tasks/" + encodeURIComponent(id) + "/context", { content: c.value }))) { c.value = ""; toast(t("saved")); }
    } } }, el("label", { for: "corr", text: t("task.correct") }), el("p", { class: "muted", text: t("task.correctHint") }), c,
      el("p", {}, el("button", { type: "submit", text: t("task.send") }))));
  }
  // Files.
  const fl = (files && files.files) || [];
  if (fl.length) parts.push(el("h2", { text: t("task.files") }), el("div", { class: "card" }, fl.map((f) => el("div", {},
    el("a", { href: "/api/v1/tasks/" + encodeURIComponent(id) + "/files/" + String(f.path).split("/").map(encodeURIComponent).join("/"),
      target: "_blank", rel: "noopener noreferrer", text: f.path }), " ", el("span", { class: "muted", text: f.bytes + " B" })))));
  // Activity (untrusted data rendered as text).
  const list = ((evs && evs.events) || []).slice(-120).reverse().map((e) => el("div", { class: "ev" },
    el("div", { class: "t", text: fmtTime(e.at) + " · " + e.actor + " · " + e.type }),
    e.data ? el("pre", { text: JSON.stringify(e.data, null, 1).slice(0, 2500) }) : null));
  parts.push(el("details", {}, el("summary", { text: t("reply.details") + " · " + t("task.events") }), el("div", {}, list)));
  render(...parts);
  if (!terminal) timer = setTimeout(() => { if (location.hash === "#/task/" + id) route(); }, 6000);
}

// ---------------------------------------------------------------- approvals (Blueprint 13A.7 rule 4)
async function routeApprovals() {
  const data = await guard(() => api("GET", "/approvals"));
  if (!data) return;
  const cards = (data.approvals || []).map((a) => {
    const code = el("input", { type: "text", inputmode: "numeric", autocomplete: "one-time-code", maxlength: 6, required: true, id: "c-" + a.id });
    const decide = (d) => async (ev) => {
      ev.preventDefault();
      const body = d === "approve" ? { action_hash: a.action_hash, code: code.value } : undefined;
      if (await guard(() => api("POST", "/approvals/" + encodeURIComponent(a.id) + "/" + d, body))) { toast(t(d === "approve" ? "ap.approved" : "ap.rejected")); route(); }
    };
    return el("form", { class: "card", on: { submit: decide("approve") } },
      el("dl", {},
        el("dt", { text: t("ap.action") }), el("dd", { text: a.tool_id + " (v" + a.tool_version + ")" }),
        el("dt", { text: t("ap.target") }), el("dd", { text: a.target || "—" }),
        el("dt", { text: t("ap.side") }), el("dd", { text: a.side_effect }),
        el("dt", { text: t("ap.tier") }), el("dd", { text: a.tier || "A2" }),
        el("dt", { text: t("ap.cost") }), el("dd", { text: (a.estimated_cost || "") + " · task so far $" + Number(a.task_cost_usd || 0).toFixed(4) }),
        el("dt", { text: t("ap.task") }), el("dd", {}, el("a", { href: "#/task/" + encodeURIComponent(a.task_id), text: a.task_objective || a.task_id })),
        el("dt", { text: t("ap.step") }), el("dd", { text: String(a.step_no || "") }),
        el("dt", { text: t("ap.why") }), el("dd", { text: a.why || "—" }),
        el("dt", { text: t("ap.expires") }), el("dd", { text: fmtTime(a.expires_at) }),
        el("dt", { text: t("ap.hash") }), el("dd", { class: "muted", text: a.action_hash })),
      el("h2", { text: t("ap.params") }), pretty(a.parameters),
      el("label", { for: "c-" + a.id, text: t("ap.code") }), code,
      el("div", { class: "row" }, el("button", { class: "primary", type: "submit", text: t("ap.approve") }),
        el("button", { type: "button", class: "danger", on: { click: decide("reject") }, text: t("ap.reject") })));
  });
  render(el("h1", { text: t("ap.title") }), cards.length ? cards : el("p", { class: "muted", text: t("ap.empty") }));
}

// ---------------------------------------------------------------- notifications, tools, self
async function routeNotifications() {
  const data = await guard(() => api("GET", "/notifications"));
  if (!data) return;
  const cards = (data.notifications || []).map((n) => el("div", { class: "card" },
    el("div", { class: "row" }, el("strong", { text: n.title }), el("span", { class: "muted", text: fmtTime(n.created_at) + (n.read_at ? " · read" : "") })),
    pretty(n.body),
    n.task_id ? el("p", {}, el("a", { href: "#/task/" + encodeURIComponent(n.task_id), text: t("task.open") })) : null,
    n.read_at ? null : el("button", { type: "button", on: { click: async () => {
      if (await guard(() => api("POST", "/notifications/" + encodeURIComponent(n.id) + "/read"))) route();
    } }, text: t("no.read") })));
  render(el("h1", { text: t("no.title") }), cards.length ? cards : el("p", { class: "muted", text: t("no.empty") }));
}

async function routeTools() {
  const data = await guard(() => api("GET", "/tools"));
  if (!data) return;
  const cards = (data.tools || []).map((tl) => {
    const code = el("input", { type: "text", inputmode: "numeric", autocomplete: "one-time-code", maxlength: 6, id: "t-" + tl.id });
    const act = (a, body) => async () => {
      if (await guard(() => api("POST", "/tools/" + encodeURIComponent(tl.id) + "/" + a, body))) route();
    };
    return el("div", { class: "card" },
      el("div", { class: "row" }, el("strong", { text: "l2." + tl.name + " v" + tl.version }), stateBadge(tl.status)),
      el("p", { text: tl.description }),
      el("details", {}, el("summary", { text: t("tools.source") + " (sha256 " + String(tl.source_sha256).slice(0, 16) + "…)" }), el("pre", { text: tl.source })),
      el("details", {}, el("summary", { text: t("tools.tests") }), pretty(tl.tests)),
      el("details", {}, el("summary", { text: t("tools.report") }), pretty(tl.report)),
      tl.status === "awaiting_approval" ? el("div", {},
        el("label", { for: "t-" + tl.id, text: t("ap.code") }), code,
        el("div", { class: "row" },
          el("button", { class: "primary", type: "button", on: { click: () => act("approve", { sha256: tl.source_sha256, code: code.value })() }, text: t("tools.approve") }),
          el("button", { class: "danger", type: "button", on: { click: act("reject", {}) }, text: t("tools.reject") }))) : null,
      tl.status === "active" ? el("button", { type: "button", on: { click: act("disable", {}) }, text: t("tools.disable") }) : null);
  });
  render(el("h1", { text: t("tools.title") }), cards.length ? cards : el("p", { class: "muted", text: t("tools.empty") }));
}

// ---------------------------------------------------------------- browser: profiles, sessions, remote view (ADR-0042)
async function routeBrowser() {
  const pr = await guard(() => api("GET", "/browser/profiles"));
  const ss = await guard(() => api("GET", "/browser/sessions"));
  if (!pr || !ss) return;
  const split = (v) => v.split(",").map((x) => x.trim()).filter(Boolean);
  const profiles = (pr.profiles || []).map((p) => {
    const u = el("input", { type: "url", placeholder: "https://" + (p.domains[0] || "") + "/login", id: "u-" + p.name });
    const x = el("input", { type: "text", id: "x-" + p.name });
    return el("form", { class: "card", on: { submit: async (ev) => {
      ev.preventDefault();
      const r = await guard(() => api("POST", "/browser/profiles/" + encodeURIComponent(p.name) + "/login", { url: u.value, extra_domains: split(x.value) }));
      if (r) location.hash = "#/screen/" + encodeURIComponent(r.session);
    } } }, el("div", { class: "row" }, el("strong", { text: p.name }), el("span", { class: "muted", text: p.domains.join(", ") + (p.session_saved ? " · " + t("br.saved") : "") })),
      el("label", { for: "u-" + p.name, text: t("br.startUrl") }), u, el("label", { for: "x-" + p.name, text: t("br.extra") }), x,
      el("p", {}, el("button", { type: "submit", text: t("br.start") })));
  });
  const sessions = (ss.sessions || []).map((s) => el("div", { class: "card row" }, el("span", { text: s.key }),
    el("span", { class: "muted", text: s.url || "" }), el("a", { href: "#/screen/" + encodeURIComponent(s.key), text: t("br.view") })));
  render(el("h1", { text: t("br.title") }), el("h2", { text: t("br.profiles") }), profiles.length ? profiles : el("p", { class: "muted", text: t("br.noProfiles") }),
    el("h2", { text: t("br.sessions") }), sessions.length ? sessions : el("p", { class: "muted", text: t("br.noSessions") }));
}

let screenURL = "";
async function routeScreen(key) {
  const img = el("img", { alt: "", width: 1280, height: 800, class: "screen" });
  const urlLine = el("p", { class: "muted" });
  const base = "/browser/sessions/" + encodeURIComponent(key);
  const refresh = async () => {
    try {
      const r = await fetch("/api/v1" + base + "/screen", { credentials: "same-origin" });
      if (!r.ok) throw new Error(String(r.status));
      urlLine.textContent = r.headers.get("X-Page-URL") || "";
      const b = await r.blob();
      if (screenURL) URL.revokeObjectURL(screenURL);
      screenURL = URL.createObjectURL(b);
      img.src = screenURL;
    } catch (e) { toast(t("err") + e.message); }
  };
  const input = async (body) => { if (await guard(() => api("POST", base + "/input", body))) setTimeout(refresh, 400); };
  img.addEventListener("click", (ev) => {
    const r = img.getBoundingClientRect();
    input({ action: "click", x: Math.round((ev.clientX - r.left) * 1280 / r.width), y: Math.round((ev.clientY - r.top) * 800 / r.height) });
  });
  const text = el("input", { type: "text", id: "rt", autocomplete: "off" });
  const go = el("input", { type: "url", id: "rg" });
  const owner = key.startsWith("owner-");
  const profile = owner ? key.slice(6) : "";
  const finish = (save) => async () => {
    if (await guard(() => api("POST", "/browser/profiles/" + encodeURIComponent(profile) + "/login/finish", { save }))) { toast(t("saved")); location.hash = "#/browser"; }
  };
  render(el("h1", { text: key }), el("p", { class: "muted", text: t("br.screenHint") }), urlLine, img,
    el("form", { class: "row", on: { submit: (ev) => { ev.preventDefault(); const v = text.value; text.value = ""; if (v) input({ action: "type", text: v }); } } },
      el("label", { for: "rt", text: t("br.type") }), text, el("button", { type: "submit", text: t("br.send") })),
    el("div", { class: "row" },
      ["Enter", "Tab", "Backspace", "Escape"].map((k) => el("button", { type: "button", on: { click: () => input({ action: "key", key: k }) }, text: k })),
      el("button", { type: "button", on: { click: () => input({ action: "scroll", direction: "up" }) }, text: t("br.up") }),
      el("button", { type: "button", on: { click: () => input({ action: "scroll", direction: "down" }) }, text: t("br.down") }),
      el("button", { type: "button", on: { click: () => input({ action: "back" }) }, text: t("br.back") }),
      el("button", { type: "button", on: { click: refresh }, text: t("br.refresh") })),
    el("form", { class: "row", on: { submit: (ev) => { ev.preventDefault(); if (go.value) input({ action: "goto", url: go.value }); } } },
      el("label", { for: "rg", text: t("br.goto") }), go, el("button", { type: "submit", text: t("br.goto") })),
    owner ? el("div", { class: "row" }, el("button", { class: "primary", type: "button", on: { click: finish(true) }, text: t("br.saveClose") }),
      el("button", { type: "button", on: { click: finish(false) }, text: t("br.closeOnly") }))
      : el("p", {}, el("button", { class: "primary", type: "button", on: { click: async () => {
        if (await guard(() => api("POST", base + "/return"))) { toast(t("br.returned")); location.hash = "#/task/" + encodeURIComponent(key); }
      } }, text: t("br.return") })));
  refresh();
  timer = setInterval(() => { if (location.hash === "#/screen/" + encodeURIComponent(key) || location.hash === "#/screen/" + key) refresh(); }, 3000);
}

async function routeSelf() {
  const data = await guard(() => api("GET", "/self"));
  if (data) render(el("h1", { text: t("self.title") }), pretty(data));
}

// ---------------------------------------------------------------- router
async function route() {
  if (timer) { clearTimeout(timer); clearInterval(timer); timer = null; }
  const h = location.hash || "#/";
  if (h.startsWith("#/setup/")) { routeSetup(decodeURIComponent(h.slice(8))); return; }
  const info = await fetch("/api/v1/web/session", { credentials: "same-origin" }).then((r) => r.json()).catch(() => null);
  if (!info || !info.authenticated) { csrf = ""; showNav(false); routeLogin(info); return; }
  csrf = info.csrf; showNav(true);
  if (h.startsWith("#/task/")) return routeTask(decodeURIComponent(h.slice(7)));
  if (h === "#/approvals") return routeApprovals();
  if (h === "#/notifications") return routeNotifications();
  if (h === "#/tools") return routeTools();
  if (h === "#/browser") return routeBrowser();
  if (h.startsWith("#/screen/")) return routeScreen(decodeURIComponent(h.slice(9)));
  if (h === "#/self") return routeSelf();
  return routeTasks();
}

window.addEventListener("hashchange", route);
window.addEventListener("DOMContentLoaded", () => {
  applyI18n();
  document.getElementById("lang").addEventListener("click", () => {
    lang = lang === "id" ? "en" : "id";
    try { localStorage.setItem("aura.lang", lang); } catch (e) { /* ignore */ }
    applyI18n(); route();
  });
  document.getElementById("logout").addEventListener("click", async () => {
    await api("POST", "/web/logout").catch(() => null);
    csrf = ""; showNav(false); location.hash = "#/login"; route();
  });
  route();
});
