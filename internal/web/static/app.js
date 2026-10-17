"use strict";
(function () {
  const $ = (id) => document.getElementById(id);
  let strings = {};
  let lang = "";
  let lastState = null;
  let itemsSig = "";
  let items = [];
  const selected = new Set();
  let sortKey = "taken", sortDir = 1;
  let logNext = 0, logLines = [];
  let settingsLoaded = false, languages = [];

  const tx = (k) => (strings[k] || k).replace(/&&/g, "&");

  async function api(path, body) {
    const opt = { credentials: "same-origin", cache: "no-store" };
    if (body !== undefined) {
      opt.method = "POST";
      opt.headers = { "Content-Type": "application/json", "X-SPMAG": "1" };
      opt.body = JSON.stringify(body);
    }
    const r = await fetch(path, opt);
    let data = null;
    try { data = await r.json(); } catch (e) { /* not JSON */ }
    if (!r.ok) throw new Error((data && data.error) || r.statusText || String(r.status));
    return data;
  }

  // ---- translations -------------------------------------------------------
  function applyStrings() {
    document.querySelectorAll("[data-i18n]").forEach((el) => { el.textContent = tx(el.dataset.i18n); });
    document.querySelectorAll("[data-i18n-aria]").forEach((el) => { el.setAttribute("aria-label", tx(el.dataset.i18nAria)); });
    document.documentElement.lang = lang || "en";
    fillLanguages();
    if (lastState) renderState(lastState, true);
  }

  async function loadStrings() {
    const d = await api("/api/strings");
    strings = d.strings; lang = d.lang;
    applyStrings();
  }

  // ---- tabs ---------------------------------------------------------------
  function showTab(name) {
    document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
    document.querySelectorAll(".panel").forEach((p) => { p.hidden = p.id !== "tab-" + name; });
    if (name === "settings" && !settingsLoaded) loadSettings();
    if (name === "log") pollLog();
  }
  document.querySelectorAll(".tab").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));

  // ---- state --------------------------------------------------------------
  const STATE_READY = 3, STATE_DOWNLOADING = 4;
  const filterMatch = {
    0: () => true,
    1: (s) => s === 0 || s === 2 || s === 3,
    2: (s) => s === 1,
    3: (s) => s === 4,
    4: (s) => s === 5,
  };

  function renderState(st, force) {
    lastState = st;
    $("btnQuit").hidden = !st.canQuit;
    if (st.lang !== lang && lang !== "") { loadStrings(); }
    $("title").textContent = st.title;
    $("detail").textContent = st.detail;
    document.title = "SPMAG Wifi Photo Receiver — " + st.title;
    $("bar").style.width = st.progress ? (st.progress.percent / 10) + "%" : "0";
    $("progText").textContent = st.progress ? st.progress.text : "";
    $("counts").textContent = st.counts;
    const ready = st.state === STATE_READY;
    $("btnMissing").disabled = !ready;
    $("btnSelected").disabled = !ready;
    $("btnRescan").disabled = !ready;
    $("btnFinish").disabled = !ready;
    $("btnStop").disabled = st.state !== STATE_DOWNLOADING;
    $("btnPause").textContent = tx(st.paused ? "web.resume" : "tray.pause");
    $("ver").textContent = "SPMAG Wifi Photo Receiver " + st.version;
    const sig = JSON.stringify(st.items) + "|" + $("filter").value + "|" + sortKey + sortDir;
    if (force || sig !== itemsSig) { itemsSig = sig; items = st.items; renderRows(); }
  }

  function cmp(a, b) {
    let x = a[sortKey], y = b[sortKey];
    if (sortKey === "name") { x = x.toUpperCase(); y = y.toUpperCase(); }
    if (x < y) return -sortDir;
    if (x > y) return sortDir;
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
  }

  function cell(tr, text, cls) {
    const td = document.createElement("td");
    td.textContent = text;
    if (cls) td.className = cls;
    tr.appendChild(td);
    return td;
  }

  function renderRows() {
    const f = filterMatch[$("filter").value] || filterMatch[0];
    const rows = items.filter((it) => f(it.status)).sort(cmp);
    const body = $("rows");
    const frag = document.createDocumentFragment();
    const present = new Set();
    for (const it of rows) {
      present.add(it.key);
      const tr = document.createElement("tr");
      tr.dataset.key = it.key;
      if (selected.has(it.key)) tr.classList.add("sel");
      const td0 = document.createElement("td");
      td0.className = "chk";
      const cb = document.createElement("input");
      cb.type = "checkbox"; cb.checked = selected.has(it.key);
      td0.appendChild(cb); tr.appendChild(td0);
      cell(tr, it.name);
      cell(tr, it.kindText);
      cell(tr, it.taken);
      cell(tr, it.sizeText, "num");
      cell(tr, it.statusText, "st-" + it.status);
      cell(tr, it.folder);
      const note = it.note ? (it.path ? it.path + "  (" + it.note + ")" : it.note) : it.path;
      const tdp = cell(tr, note, "path"); tdp.title = note;
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
    for (const k of Array.from(selected)) if (!present.has(k)) selected.delete(k);
    $("empty").hidden = rows.length > 0;
    document.querySelectorAll("th[data-sort]").forEach((th) => {
      th.classList.toggle("sorted", th.dataset.sort === sortKey);
      th.classList.toggle("desc", th.dataset.sort === sortKey && sortDir < 0);
    });
    $("selAll").checked = rows.length > 0 && rows.every((it) => selected.has(it.key));
  }

  $("rows").addEventListener("click", (e) => {
    const tr = e.target.closest("tr");
    if (!tr) return;
    const k = tr.dataset.key;
    if (selected.has(k)) selected.delete(k); else selected.add(k);
    tr.classList.toggle("sel", selected.has(k));
    tr.querySelector("input").checked = selected.has(k);
    $("selAll").checked = Array.from($("rows").children).every((r) => selected.has(r.dataset.key));
  });
  $("selAll").addEventListener("change", (e) => {
    for (const tr of $("rows").children) {
      if (e.target.checked) selected.add(tr.dataset.key); else selected.delete(tr.dataset.key);
    }
    renderRows();
  });
  document.querySelectorAll("th[data-sort]").forEach((th) => th.addEventListener("click", () => {
    if (sortKey === th.dataset.sort) sortDir = -sortDir; else { sortKey = th.dataset.sort; sortDir = 1; }
    renderRows();
  }));
  $("filter").addEventListener("change", renderRows);

  // ---- actions ------------------------------------------------------------
  async function act(action, extra) {
    try { await api("/api/action", Object.assign({ action }, extra || {})); poll(); }
    catch (e) { alert(e.message); }
  }
  $("btnMissing").onclick = () => act("downloadMissing");
  $("btnSelected").onclick = () => act("downloadSelected", { keys: Array.from(selected) });
  $("btnStop").onclick = () => act("stop");
  $("btnRescan").onclick = () => act("rescan");
  $("btnFinish").onclick = () => act("finish");
  $("btnPause").onclick = () => act(lastState && lastState.paused ? "resume" : "pause");
  $("btnForget").onclick = () => act("forget", { keys: Array.from(selected) });
  $("btnIP").onclick = () => { const ip = $("ip").value.trim(); if (ip) act("connectIP", { ip }); };
  $("ip").addEventListener("keydown", (e) => { if (e.key === "Enter") $("btnIP").click(); });

  // ---- settings -----------------------------------------------------------
  function fillLanguages() {
    const sel = $("language");
    const cur = sel.dataset.value !== undefined ? sel.dataset.value : sel.value;
    sel.replaceChildren();
    const o = document.createElement("option");
    o.value = ""; o.textContent = tx("set.langAuto"); sel.appendChild(o);
    for (const l of languages) {
      const e = document.createElement("option");
      e.value = l.code; e.textContent = l.name; sel.appendChild(e);
    }
    sel.value = cur || "";
    sel.dataset.value = sel.value;
  }
  $("language").addEventListener("change", () => { $("language").dataset.value = $("language").value; });

  function fillForm(s) {
    $("destDir").value = s.destDir; $("pattern").value = s.folderPattern;
    for (const k of ["jpeg", "raw", "video", "other", "auto", "finish", "match", "pmh"]) $(k).checked = !!s[k];
    $("guid").value = s.guid; $("hostName").value = s.hostName;
    languages = s.languages || languages;
    $("language").dataset.value = s.language || "";
    fillLanguages();
    $("pairBox").hidden = !s.usbSupported;
    $("allowed").value = (s.allowedCameras || []).join("\n");
    fillWeb(s);
  }

  const webFields = ["webEnabled", "webPort", "webUser", "webPassword", "webClear", "webRemote"];
  let listenPort = location.port;

  function fillWeb(s) {
    $("webEnabled").checked = !!s.webEnabled; $("webPort").value = s.webPort;
    $("webUser").value = s.webUser; $("webRemote").checked = !!s.webRemote;
    $("webPassword").value = ""; $("webClear").checked = false;
    $("webPassword").placeholder = s.webPasswordSet ? tx("web.passKeep") : "";
    $("webClearRow").hidden = !s.webPasswordSet;
    for (const k of webFields) $(k).disabled = !!s.webLocked;
    $("webLockedNote").hidden = !s.webLocked;
    const st = s.webStatus || {};
    $("webStatus").textContent = st.error ? tx("set.webError").replace("%s", st.error) : st.running ? tx("set.webRunning").replace("%s", "http://" + st.addr) : tx("set.webStopped");
  }

  async function loadSettings() {
    try {
      const s = await api("/api/settings");
      const dl = $("patterns"); dl.replaceChildren();
      for (const p of ["{yyyy}/{yyyy}-{MM}-{dd}", "{yyyy}-{MM}-{dd}", "{yyyy}/{MM}", "{camera}/{yyyy}-{MM}-{dd}"]) {
        const o = document.createElement("option"); o.value = p; dl.appendChild(o);
      }
      fillForm(s); settingsLoaded = true;
    } catch (e) { say(e.message, true); }
  }

  function say(text, err) {
    const m = $("saveMsg"); m.textContent = text; m.className = "msg" + (err ? " err" : "");
  }

  $("form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const body = { destDir: $("destDir").value, folderPattern: $("pattern").value, hostName: $("hostName").value, language: $("language").value };
    for (const k of ["jpeg", "raw", "video", "other", "auto", "finish", "match", "pmh"]) body[k] = $(k).checked;
    body.allowedCameras = $("allowed").value.split("\n");
    Object.assign(body, {
      webEnabled: $("webEnabled").checked, webPort: parseInt($("webPort").value, 10) || 0,
      webUser: $("webUser").value, webPassword: $("webPassword").value,
      webClearPassword: $("webClear").checked, webRemote: $("webRemote").checked,
    });
    try {
      const s = await api("/api/settings", body);
      await loadStrings();
      fillForm(s);
      say(tx("web.saved"), false);
      if (s.webRestart && s.webEnabled && String(s.webPort) !== listenPort) {
        // the server moves to another port: follow it
        setTimeout(() => { location.href = location.protocol + "//" + location.hostname + ":" + s.webPort + "/"; }, 1200);
      }
      poll();
    } catch (err) { say(err.message, true); }
  });

  $("btnNewID").onclick = async () => {
    if (!confirm(tx("dlg.newIDQuestion"))) return;
    try { await api("/api/action", { action: "newGUID" }); fillForm(await api("/api/settings")); }
    catch (e) { say(e.message, true); }
  };

  $("btnPair").onclick = async () => {
    $("btnPair").disabled = true; say("…", false);
    try { const r = await api("/api/pair", {}); say(r.message, false); }
    catch (e) { say(e.message, true); }
    finally { $("btnPair").disabled = false; }
  };

  // ---- log ----------------------------------------------------------------
  async function pollLog() {
    if ($("tab-log").hidden) return;
    try {
      const d = await api("/api/log?since=" + logNext);
      logNext = d.next;
      if (d.lines.length) {
        logLines = logLines.concat(d.lines).slice(-1500);
        const el = $("log");
        const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        el.textContent = logLines.join("\n");
        if (atEnd) el.scrollTop = el.scrollHeight;
      }
    } catch (e) { /* the banner shows connection problems */ }
  }

  // ---- polling ------------------------------------------------------------
  let busy = false;
  let stopped = false;
  $("btnQuit").onclick = async () => {
    if (!confirm(tx("web.quitConfirm"))) return;
    try { await api("/api/quit", {}); } catch (e) { /* the server may close the connection */ }
    stopped = true;
    $("banner").textContent = tx("web.quitDone");
    $("banner").hidden = false;
  };
  async function poll() {
    if (busy || stopped) return;
    busy = true;
    try {
      const st = await api("/api/state");
      $("banner").hidden = true;
      renderState(st, false);
    } catch (e) {
      $("banner").textContent = tx("web.connLost");
      $("banner").hidden = false;
    } finally { busy = false; }
  }
  function loop() {
    if (!stopped) { poll(); pollLog(); }
    setTimeout(loop, document.hidden ? 5000 : 1000);
  }

  loadStrings().catch(() => {}).finally(() => { loadSettings(); loop(); });
})();
