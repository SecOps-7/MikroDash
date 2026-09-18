var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// testdata/.area-single-entry.ts
var area_single_entry_exports = {};
__export(area_single_entry_exports, {
  AREAS: () => AREAS,
  initAreaPages: () => initAreaPages
});
module.exports = __toCommonJS(area_single_entry_exports);

// web/src/dom.ts
function esc(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
}
function el(id) {
  return document.getElementById(id);
}
function resRow(id, identity, resource) {
  if (!id) return "";
  return ` data-id="${esc(id)}" data-identity="${esc(identity == null ? "" : identity)}"` + (resource ? ` data-res="${esc(resource)}"` : "");
}

// web/src/resource.ts
var schemas = /* @__PURE__ */ new Map();
var waiting = /* @__PURE__ */ new Map();
var sock = null;
var hist = /* @__PURE__ */ new Map();
var current = null;
var shown = null;
var extras = /* @__PURE__ */ new Map();
var lastSave = null;
var retry = null;
var wired = false;
var addsWired = false;
var rowsWired = false;
function schemaFor(key) {
  const cached = schemas.get(key);
  if (cached) return Promise.resolve(cached);
  return new Promise((ok) => {
    const q = waiting.get(key);
    if (q) {
      q.push(ok);
      return;
    }
    waiting.set(key, [ok]);
    sock?.emit("res:schema", { resource: key });
  });
}
function selectHtml(f, id, value, list) {
  const opts = list.slice();
  const cur = value === void 0 || value === null ? "" : String(value);
  if (cur && opts.indexOf(cur) === -1) opts.unshift(cur);
  return '<select class="sform-input" id="' + id + '">' + (f.required ? "" : '<option value=""></option>') + opts.map((o) => '<option value="' + esc(o) + '"' + (String(value) === o ? " selected" : "") + ">" + esc(o) + "</option>").join("") + "</select>";
}
function checkedValue(value) {
  return value === true || value === "true" || value === "yes";
}
function fieldHtml(f, value, choices) {
  const id = "resf_" + f.name;
  if (f.display) {
    const v = value === void 0 || value === null ? "" : String(value);
    return '<div style="margin-top:.6rem" data-res-field="' + esc(f.name) + '"><label class="sform-label" for="' + id + '">' + esc(f.label) + '</label><input class="sform-input" id="' + id + '" value="' + esc(v) + '" disabled></div>';
  }
  if (f.input === "checkbox") {
    return '<label class="stoggle" style="margin-top:.7rem" data-res-field="' + esc(f.name) + '"><span class="stoggle-label">' + esc(f.label) + '</span><span class="stoggle-switch"><input type="checkbox" id="' + id + '"' + (checkedValue(value) ? " checked" : "") + '><span class="stoggle-track"></span><span class="stoggle-thumb"></span></span></label>';
  }
  if (f.input === "code") {
    const code = value === void 0 || value === null ? "" : String(value);
    return '<div style="margin-top:.6rem" data-res-field="' + esc(f.name) + '"><label class="sform-label" for="' + id + '">' + esc(f.label) + '</label><textarea class="sform-input" id="' + id + '" rows="12" spellcheck="false" style="font-family:var(--font-mono,monospace);font-size:.74rem;white-space:pre;resize:vertical">' + esc(code) + "</textarea>" + (f.help ? '<div style="font-size:.66rem;color:var(--text-muted);margin-top:.15rem">' + esc(f.help) + "</div>" : "") + "</div>";
  }
  if (f.input === "multi") {
    const chosen = new Set(String(value ?? "").split(",").map((x) => x.trim()).filter(Boolean));
    const boxes = (f.options || []).map((o, i) => '<label style="display:flex;align-items:center;gap:.35rem;font-size:.78rem"><input type="checkbox" id="' + id + "__" + i + '" data-res-multi="' + esc(f.name) + '" value="' + esc(o) + '"' + (chosen.has(o) ? " checked" : "") + ">" + esc(o) + "</label>").join("");
    return '<div style="margin-top:.6rem" data-res-field="' + esc(f.name) + '"><div class="sform-label">' + esc(f.label) + '</div><div id="' + id + '" style="display:grid;grid-template-columns:repeat(auto-fill,minmax(130px,1fr));gap:.3rem .6rem;padding:.5rem;border:1px solid var(--border);border-radius:6px">' + boxes + "</div>" + (f.help ? '<div style="font-size:.66rem;color:var(--text-muted);margin-top:.15rem">' + esc(f.help) + "</div>" : "") + "</div>";
  }
  const lbl = '<label class="sform-label" for="' + id + '">' + esc(f.label) + (f.required ? ' <span style="color:var(--accent-err)">*</span>' : "") + "</label>";
  const help = f.help ? '<div style="font-size:.66rem;color:var(--text-muted);margin-top:.15rem">' + esc(f.help) + "</div>" : "";
  let body;
  if (choices && choices.length) {
    body = selectHtml(f, id, value, choices);
  } else if (f.input === "select") {
    body = selectHtml(f, id, value, f.options || []);
  } else {
    let attrs = ' type="' + esc(f.input) + '"';
    if (f.min !== null && f.min !== void 0) attrs += ' min="' + esc(f.min) + '"';
    if (f.max !== null && f.max !== void 0 && f.input === "number") attrs += ' max="' + esc(f.max) + '"';
    const v = f.type === "secret" ? "" : value === void 0 || value === null ? "" : value;
    body = '<input class="sform-input" id="' + id + '"' + attrs + ' value="' + esc(v) + '" placeholder="' + esc(f.placeholder || "") + '" autocomplete="off">';
  }
  return '<div style="margin-top:.6rem" data-res-field="' + esc(f.name) + '">' + lbl + body + help + "</div>";
}
function applyShowIf(schema) {
  const host = el("res_fields");
  if (!host) return;
  for (const f of schema.fields) {
    if (!f.showIf) continue;
    const ctl = el("resf_" + f.showIf.field);
    const wrap = host.querySelector('[data-res-field="' + f.name + '"]');
    if (!ctl || !wrap) continue;
    wrap.style.display = f.showIf.in.indexOf(String(ctl.value)) !== -1 ? "" : "none";
  }
}
function buildForm(schema, values, options) {
  const host = el("res_fields");
  if (!host) return;
  host.innerHTML = schema.fields.map((f) => fieldHtml(f, values ? values[f.name] : void 0, options ? options[f.name] : void 0)).join("");
  applyShowIf(schema);
  const seen = /* @__PURE__ */ new Set();
  for (const f of schema.fields) {
    if (!f.showIf || seen.has(f.showIf.field)) continue;
    seen.add(f.showIf.field);
    el("resf_" + f.showIf.field)?.addEventListener("change", () => applyShowIf(schema));
  }
}
function readValues(schema) {
  const out = {};
  for (const f of schema.fields) {
    if (f.display) continue;
    const node = el("resf_" + f.name);
    if (!node) continue;
    if (f.input === "multi") {
      out[f.name] = Array.from(node.querySelectorAll("input[data-res-multi]")).filter((b) => b.checked).map((b) => b.value).join(",");
      continue;
    }
    out[f.name] = f.input === "checkbox" ? String(node.checked) : node.value;
  }
  return out;
}
function setError(msg) {
  const box = el("res_error");
  if (!box) return;
  box.textContent = msg;
  box.style.display = msg ? "" : "none";
}
function close() {
  el("resModal")?.classList.remove("open");
  const warn = el("res_warn");
  if (warn) warn.style.display = "none";
  current = null;
  retry = null;
  lastSave = null;
}
function warningText(code, w) {
  const str = (v) => typeof v === "string" ? v : "";
  const cut = "This may cut MikroDash off from this router.";
  switch (code) {
    case "self-cutoff":
      return {
        headline: cut,
        why: "The router sees MikroDash at <code>" + esc(str(w.address) || "?") + "</code>, which arrives on <code>" + esc(str(w.interface) || "?") + "</code>, the interface this change " + (str(w.action) === "delete" ? "removes" : "alters") + "."
      };
    case "route-cutoff": {
      const verb = str(w.action) === "delete" ? "removes" : str(w.action) === "create" ? "adds" : "changes";
      return {
        headline: cut,
        why: "The router sees MikroDash at <code>" + esc(str(w.address) || "?") + "</code>, an address it reaches through a route, and this change " + verb + " the route <code>" + esc(str(w.destination) || "?") + "</code> that covers it."
      };
    }
    case "route-cutoff-unknown":
      return {
        headline: cut,
        why: "MikroDash could not read where the router sees it connecting from, so it cannot tell whether changing the route <code>" + esc(str(w.destination) || "?") + "</code> cuts its own connection."
      };
    case "address-cutoff": {
      const verb = str(w.action) === "delete" ? "removes" : "changes";
      return {
        headline: cut,
        why: "The router sees MikroDash at <code>" + esc(str(w.address) || "?") + "</code>, on the subnet of <code>" + esc(str(w.prefix) || "?") + "</code>" + (str(w.interface) ? " on <code>" + esc(str(w.interface)) + "</code>" : "") + ", and this change " + verb + " that address."
      };
    }
    case "address-cutoff-unknown":
      return {
        headline: cut,
        why: "MikroDash could not read where the router sees it connecting from, so it cannot tell whether changing the address <code>" + esc(str(w.prefix) || "?") + "</code> cuts its own connection."
      };
    case "list-cutoff": {
      const v = "<code>" + esc(str(w.value) || "?") + "</code>";
      const who = str(w.kind) === "interface" ? "the interface MikroDash arrives on, " + v + "," : v + ", which covers the address the router sees MikroDash at,";
      return {
        headline: cut,
        why: "This " + (str(w.move) === "joins" ? "puts " : "takes ") + who + " " + (str(w.move) === "joins" ? "into" : "out of") + " the list <code>" + esc(str(w.list) || "?") + "</code>, and the input rule matching <code>" + esc(str(w.ruleMatch) || "?") + "</code> would then " + (str(w.effect) === "loses-accept" ? "stop accepting" : "drop") + " its traffic."
      };
    }
    case "list-redefine": {
      const verb = str(w.change) === "delete" ? "deletes" : str(w.change) === "rename" ? "renames" : "changes what is in";
      return {
        headline: cut,
        why: "This " + verb + " the list <code>" + esc(str(w.list) || "?") + "</code>, which the input rule matching <code>" + esc(str(w.ruleMatch) || "?") + "</code> uses to decide on MikroDash's own traffic."
      };
    }
    case "self-lockout":
      return { headline: cut, why: "This firewall rule could match MikroDash's own traffic to the router." };
    case "self-throttle": {
      const cap = w.maxLimit && typeof w.maxLimit === "object" ? w.maxLimit : {};
      const rate = (v) => {
        if (typeof v !== "number") return "?";
        if (v === 0) return "unlimited";
        if (v >= 1e9 && v % 1e9 === 0) return v / 1e9 + "G";
        if (v >= 1e6 && v % 1e6 === 0) return v / 1e6 + "M";
        if (v >= 1e3 && v % 1e3 === 0) return v / 1e3 + "k";
        return String(v);
      };
      return {
        headline: "This queue covers MikroDash's own connection to this router.",
        why: "The router sees MikroDash at <code>" + esc(str(w.address) || "?") + "</code>, inside <code>" + esc(str(w.target) || "?") + "</code>, and this queue caps it at <code>" + esc(rate(cap.up) + "/" + rate(cap.down)) + "</code>. The dashboard may become slow; the queue can still be edited or removed from its row."
      };
    }
    case "wifi-inherit":
      return { headline: "This change needs confirming.", why: "It overrides a setting this network inherits from a shared configuration profile." };
    case "capsman-push":
      return { headline: "This change needs confirming.", why: "This profile is provisioned to managed access points, so the change will be pushed to them." };
    default:
      return { headline: "This change needs confirming.", why: "A safety check flagged it (<code>" + esc(code || "?") + "</code>)." };
  }
}
function guardRefusedText(rule) {
  switch (rule) {
    case "protected-account":
      return "That is the account MikroDash signs in with. Manage it in WinBox.";
    case "protected-group":
      return "That is the group MikroDash signs in with. Manage it in WinBox.";
    case "protected-name-value":
      return "That name belongs to the account MikroDash signs in with.";
    case "protected-group-value":
      return "Users cannot be placed in, or edited while in, the group MikroDash signs in with.";
    case "self-unresolved":
      return "MikroDash cannot identify its own account on this router, so changes are refused.";
    case "service-disable":
      return "That is the API service MikroDash connects through. Disabling it would cut MikroDash off; change it in WinBox.";
    case "service-port":
      return "That is the API service MikroDash connects through. Moving its port would cut MikroDash off; change it in WinBox.";
    case "service-vrf":
      return "That is the API service MikroDash connects through. Moving it to another VRF would cut MikroDash off; change it in WinBox.";
    case "service-address":
      return "That address list would not admit the address the router sees MikroDash connecting from.";
    case "code-requires-admin":
      return "Changing or running RouterOS code is limited to global administrators, as raw commands are.";
    case "certificate-in-use":
      return "That is the certificate the API service MikroDash connects through presents. Removing it would cut MikroDash off; change it in WinBox.";
    case "certificate-unknown":
      return "MikroDash cannot read which certificate its API service presents, so removing certificates is refused.";
    case "service-address-unknown":
      return "MikroDash cannot read where the router sees it connecting from, so it cannot show that address list would still admit it.";
    default:
      return "A safety rule refused this change.";
  }
}
var warnCode = "";
function showWarning(d) {
  const box = el("res_warn");
  if (!box) return;
  const w = d.warning ?? {};
  if (d.code && d.code !== "stale-warning") warnCode = d.code;
  const { headline, why } = warningText(warnCode, w);
  box.innerHTML = "<strong>" + headline + "</strong><br>" + why + (d.code === "stale-warning" ? "<br><em>The values changed since you were asked, so please confirm again.</em>" : "") + '<div style="display:flex;gap:.4rem;justify-content:flex-end;margin-top:.5rem"><button class="sbtn sbtn-outline" id="res_warnCancel" style="padding:.3rem .7rem;font-size:.72rem">Cancel</button><button class="sbtn sbtn-danger" id="res_warnGo" style="padding:.3rem .7rem;font-size:.72rem">Do it anyway</button></div>';
  box.style.display = "";
  const ack = d.fingerprint || "";
  const again = retry;
  el("res_warnCancel")?.addEventListener("click", () => {
    box.style.display = "none";
    retry = null;
  });
  el("res_warnGo")?.addEventListener("click", () => {
    box.style.display = "none";
    again?.(ack);
  });
}
function actionBar(schema, avail) {
  const host = el("res_actions");
  if (!host) return;
  host.innerHTML = (avail || []).map((k) => {
    const a = (schema.actions || []).find((x) => x.key === k);
    return a ? '<button class="sbtn sbtn-primary" data-res-actionbtn="' + esc(a.key) + '" style="padding:.3rem .7rem;font-size:.72rem">' + esc(a.label) + "</button>" : "";
  }).join(" ");
}
function show(schema, values, row, readOnly, options, actions, removable) {
  current = { key: schema.key, id: row ? row.id : null, identity: row ? row.identity : null };
  shown = { schema, options: options || {} };
  const title = el("res_title");
  if (title) title.textContent = (readOnly ? "" : row ? "Edit " : "Add ") + schema.title;
  buildForm(schema, values, options);
  actionBar(schema, actions || []);
  lastSave = null;
  const slot = el("res_extra");
  if (slot) {
    const extra = readOnly ? void 0 : extras.get(schema.key);
    const html = extra ? extra.render(row ? "edit" : "add") : "";
    slot.innerHTML = html;
    slot.style.display = html ? "" : "none";
    if (html) extra.wire?.();
  }
  if (readOnly) {
    el("res_fields")?.querySelectorAll("input,select").forEach((n) => {
      n.disabled = true;
    });
  }
  setError("");
  const warn = el("res_warn");
  if (warn) warn.style.display = "none";
  const prev = el("res_preview");
  if (prev) prev.style.display = "none";
  const del = el("res_delete");
  if (del) del.style.display = row && !readOnly && removable !== false ? "" : "none";
  const dup = el("res_dup");
  if (dup) dup.style.display = row && !readOnly && schema.creatable !== false ? "" : "none";
  const save = el("res_save");
  if (save) {
    save.style.display = readOnly ? "none" : "";
    save.textContent = row ? "Save" : "Add " + schema.label;
  }
  const pbtn = el("res_previewBtn");
  if (pbtn) pbtn.style.display = readOnly ? "none" : "";
  el("resModal")?.classList.add("open");
}
function histButton(kind, key, on, label) {
  const glyph = kind === "undo" ? "\u21B6" : "\u21B7";
  const title = on ? (kind === "undo" ? "Undo " : "Redo ") + label : "Nothing to " + kind;
  return '<button class="sbtn ' + (on ? "sbtn-primary" : "sbtn-ghost") + ' res-hist" data-res-hist="' + kind + '" data-res-histkey="' + esc(key) + '"' + (on ? "" : " disabled") + ' title="' + esc(title) + '">' + glyph + "</button>";
}
function histTarget(ready) {
  for (const s of ready) {
    const h = hist.get(s.key);
    if (h && (h.canUndo || h.canRedo)) return s.key;
  }
  return ready[0]?.key ?? "";
}
var pendingMove = null;
function doMove(ack) {
  if (!pendingMove) return;
  retry = doMove;
  sock?.emit("res:move", ack ? { ...pendingMove, ack } : { ...pendingMove });
}
var drag = null;
function rowUnder(host, x, y) {
  const hit = document.elementFromPoint(x, y);
  const row = hit?.closest?.("tr[data-id], tr.res-drag-origin");
  return row && host.contains(row) ? row : null;
}
function makeOriginMarker(row) {
  const tr = document.createElement("tr");
  tr.className = "res-drag-origin";
  const td = document.createElement("td");
  td.colSpan = row.children.length || 1;
  td.style.height = row.getBoundingClientRect().height + "px";
  tr.appendChild(td);
  return tr;
}
function syncOriginMarker() {
  if (!drag || !drag.marker) return;
  drag.marker.classList.toggle("is-home", drag.marker.nextElementSibling === drag.row);
}
function dragTo(x, y) {
  if (!drag) return;
  if (!drag.host.contains(drag.row)) {
    endDrag();
    return;
  }
  const over = rowUnder(drag.host, x, y);
  if (!over || over === drag.row) return;
  if (!drag.marker) {
    drag.marker = makeOriginMarker(drag.row);
    drag.row.parentNode?.insertBefore(drag.marker, drag.row);
  }
  if (over === drag.marker) {
    drag.host.insertBefore(drag.row, drag.marker.nextSibling);
  } else {
    const above = drag.row.compareDocumentPosition(over) & Node.DOCUMENT_POSITION_PRECEDING;
    drag.host.insertBefore(drag.row, above ? over : over.nextSibling);
  }
  syncOriginMarker();
}
function endDrag() {
  if (!drag) return null;
  const d = drag;
  drag = null;
  if (d.raf) cancelAnimationFrame(d.raf);
  d.marker?.parentNode?.removeChild(d.marker);
  d.marker = null;
  d.row.classList.remove("res-dragging");
  document.body.classList.remove("res-dragging-body");
  return d;
}
function anchorAfter(row) {
  let next = row.nextElementSibling;
  while (next && !next.getAttribute("data-id")) next = next.nextElementSibling;
  return next ? next.getAttribute("data-id") || "" : "";
}
function wireDrag() {
  document.addEventListener("pointerdown", (e) => {
    const t = e.target;
    if (!t?.closest) return;
    const h = t.closest("[data-res-drag]");
    if (!h) return;
    const row = h.closest("[data-id]");
    const host = h.closest("[data-res-rows]");
    if (!row || !host) return;
    const key = row.getAttribute("data-res") || host.getAttribute("data-res-rows") || "";
    const schema = schemas.get(key);
    if (!schema || !schema.permitted) return;
    e.preventDefault();
    drag = { host, row, key, raf: 0, x: e.clientX, y: e.clientY, marker: null };
    row.classList.add("res-dragging");
    document.body.classList.add("res-dragging-body");
    try {
      h.setPointerCapture(e.pointerId);
    } catch {
    }
  });
  document.addEventListener("pointermove", (e) => {
    if (!drag) return;
    drag.x = e.clientX;
    drag.y = e.clientY;
    if (drag.raf) return;
    drag.raf = requestAnimationFrame(() => {
      if (!drag) return;
      drag.raf = 0;
      dragTo(drag.x, drag.y);
    });
  });
  document.addEventListener("pointercancel", () => {
    endDrag();
  });
  document.addEventListener("pointerup", () => {
    if (!drag) return;
    dragTo(drag.x, drag.y);
    const d = endDrag();
    if (!d || !d.host.contains(d.row)) return;
    pendingMove = {
      resource: d.key,
      id: d.row.getAttribute("data-id") || "",
      expectedIdentity: d.row.getAttribute("data-identity") || void 0,
      anchor: anchorAfter(d.row)
    };
    doMove("");
  });
}
function doHist(kind, key, ack) {
  if (!kind || !key) return;
  retry = (a) => doHist(kind, key, a);
  sock?.emit("res:" + kind, ack ? { resource: key, ack } : { resource: key });
}
var routerSelected = false;
function mountAddSlots() {
  document.querySelectorAll("[data-res-add]").forEach((host) => {
    const keys = (host.getAttribute("data-res-add") || "").split(",").map((k) => k.trim()).filter(Boolean);
    const ready = keys.map((k) => schemas.get(k)).filter((s) => !!s && s.permitted);
    const target = ready.length ? histTarget(ready) : "";
    const h = target && hist.get(target) || null;
    host.innerHTML = (target ? histButton("undo", target, !!h?.canUndo, h?.undoLabel || "") + histButton("redo", target, !!h?.canRedo, h?.redoLabel || "") : "") + // NO ADD FOR A RESOURCE THAT CANNOT BE CREATED. A hand-built page simply
    // has no slot for one, so this never came up until the generated pages,
    // whose shell always has a slot: Certificates and IP Services offered an
    // Add the server would refuse (found on the CHR, 2026-09-18). Undo and redo
    // still belong: those rows can be edited.
    ready.filter((s) => s.creatable !== false).map((s) => '<button class="sbtn sbtn-primary" data-res-addbtn="' + esc(s.key) + '" style="padding:.28rem .65rem;font-size:.72rem">' + esc("+ Add " + s.label) + "</button>").join("");
  });
}
function mountAdds(socket) {
  sock = socket;
  wire(socket);
  if (routerSelected) {
    document.querySelectorAll("[data-res-add]").forEach((host) => {
      (host.getAttribute("data-res-add") || "").split(",").forEach((k) => {
        const key = k.trim();
        if (key) schemaFor(key);
      });
    });
  }
  mountAddSlots();
  if (addsWired) return;
  addsWired = true;
  document.addEventListener("click", (ev) => {
    const target = ev.target;
    if (!target.closest) return;
    const hb = target.closest("[data-res-hist]");
    if (hb) {
      if (hb.disabled) return;
      doHist(
        hb.getAttribute("data-res-hist") || "",
        hb.getAttribute("data-res-histkey") || "",
        ""
      );
      return;
    }
    const act = target.closest("[data-res-actionbtn]");
    if (act && current) {
      const key = act.getAttribute("data-res-actionbtn") || "";
      const send = (ack) => {
        socket.emit("res:action", {
          resource: current.key,
          action: key,
          id: current.id || void 0,
          expectedIdentity: current.identity || void 0,
          ack: ack || void 0
        });
      };
      retry = send;
      send("");
      return;
    }
    const btn = target.closest("[data-res-addbtn]");
    if (!btn) return;
    openResource(socket, btn.getAttribute("data-res-addbtn") || "", null);
  });
}
function mountRows(socket) {
  sock = socket;
  wire(socket);
  if (rowsWired) return;
  rowsWired = true;
  wireDrag();
  document.addEventListener("click", (ev) => {
    const target = ev.target;
    if (!target.closest) return;
    const mv = target.closest("[data-res-move]");
    if (mv) {
      if (mv.disabled) return;
      const mrow = target.closest("[data-id]");
      const mhost = target.closest("[data-res-rows]");
      if (!mrow || !mhost) return;
      const mkey = mrow.getAttribute("data-res") || mhost.getAttribute("data-res-rows") || "";
      const mschema = schemas.get(mkey);
      if (!mschema || !mschema.permitted) return;
      pendingMove = {
        resource: mkey,
        id: mrow.getAttribute("data-id") || "",
        expectedIdentity: mrow.getAttribute("data-identity") || void 0,
        direction: mv.getAttribute("data-res-move") || ""
      };
      doMove("");
      return;
    }
    const host = target.closest("[data-res-rows]");
    if (!host) return;
    const row = target.closest("[data-id]");
    if (!row || !host.contains(row)) return;
    const key = row.getAttribute("data-res") || host.getAttribute("data-res-rows") || "";
    if (!key) return;
    openResource(socket, key, {
      id: row.getAttribute("data-id") || "",
      name: row.getAttribute("data-identity") || ""
    });
  });
}
function openResource(socket, key, row) {
  sock = socket;
  wire(socket);
  schemaFor(key).then((schema) => {
    if (!schema.permitted) return;
    if (!row) {
      current = null;
      socket.emit("res:new", { resource: key });
      return;
    }
    current = { key, id: row.id, identity: row.name ?? null };
    socket.emit("res:row", { resource: key, id: row.id, expectedIdentity: row.name || void 0 });
  }).catch((e) => {
    console.error(e);
    setError(String(e.message || e));
  });
}
function wire(socket) {
  if (wired) return;
  wired = true;
  socket.on("res:row", (d) => {
    const schema = schemas.get(d.resource);
    if (!schema) return;
    show(
      schema,
      d.values || {},
      { id: d.id, identity: d.identity },
      !!d.readOnly,
      d.options || {},
      d.actions || [],
      d.removable !== false
    );
  });
  socket.on("res:history", (d) => {
    if (!d || !d.resource) return;
    hist.set(d.resource, d);
    mountAddSlots();
  });
  socket.on("res:schema", (d) => {
    if (!d || !d.key) return;
    schemas.set(d.key, d);
    mountAddSlots();
    const q = waiting.get(d.key) || [];
    waiting.delete(d.key);
    q.forEach((cb) => cb(d));
  });
  const refreshAll = () => {
    routerSelected = true;
    schemas.clear();
    waiting.clear();
    mountAddSlots();
    document.querySelectorAll("[data-res-add]").forEach((host) => {
      (host.getAttribute("data-res-add") || "").split(",").forEach((k) => {
        const key = k.trim();
        if (key) schemaFor(key);
      });
    });
  };
  socket.on("router:switched", refreshAll);
  socket.on("disconnect", () => {
    routerSelected = false;
  });
  document.addEventListener("mikrodash:resmount", () => {
    document.querySelectorAll("[data-res-add]").forEach((host) => {
      (host.getAttribute("data-res-add") || "").split(",").forEach((k) => {
        const key = k.trim();
        if (key) schemaFor(key);
      });
    });
    mountAddSlots();
  });
  socket.on("res:new", (d) => {
    const schema = schemas.get(d.resource);
    if (!schema) return;
    show(schema, null, null, false, d.options || {}, []);
  });
  socket.on("res:ok", () => {
    const done = lastSave;
    lastSave = null;
    close();
    if (done) extras.get(done.key)?.saved?.(done.values);
  });
  const routerSaid = { "router-denied": true, "write-failed": true };
  socket.on("res:error", (d) => {
    if (d && (d.fingerprint || d.code === "stale-warning")) {
      showWarning(d);
      return;
    }
    const codes = {
      denied: "You may not change this.",
      unavailable: "The router is not reachable.",
      "stale-row": "That row changed on the router. Close and reopen it.",
      "read-only-row": "This entry cannot be edited here.",
      "not-creatable": "This cannot be added here.",
      "not-removable": "This entry cannot be removed here.",
      "router-denied": "The router refused the change: the API user lacks permission.",
      "write-failed": "The router refused the change.",
      "bad-request": "That request was incomplete.",
      "guard-not-ported": "This change needs a safety check that is not available yet, so it was refused.",
      "guard-refused": guardRefusedText(d && d.rule),
      "rate-limited": "Too many changes to this router in the last minute. Wait a moment and try again.",
      "outcome-unknown": "The router accepted the change, but it could not be confirmed. The table has been refreshed; check it before trying again."
    };
    if (d && d.code === "invalid" && Array.isArray(d.errors)) {
      setError(d.errors.map((e) => e.message).join("; "));
      return;
    }
    const said = d && d.message || "";
    const mapped = d && codes[d.code] || "";
    setError(mapped ? mapped + (routerSaid[d.code] && said && said !== mapped ? " Router: " + said : "") : said || "The change was refused.");
  });
  el("res_previewBtn")?.addEventListener("click", () => {
    if (!current) return;
    const schema = schemas.get(current.key);
    if (!schema) return;
    socket.emit("res:preview", {
      resource: current.key,
      id: current.id || "",
      values: readValues(schema)
    });
  });
  socket.on("res:preview", (d) => {
    if (!d || !current || d.resource !== current.key) return;
    const p = el("res_preview");
    if (!p) return;
    p.textContent = d.command || "";
    p.style.display = "";
  });
  el("res_save")?.addEventListener("click", () => {
    if (!current) return;
    const schema = schemas.get(current.key);
    if (!schema) return;
    setError("");
    const body = {
      resource: current.key,
      id: current.id || "",
      expectedIdentity: current.identity || "",
      values: readValues(schema)
    };
    lastSave = { key: body.resource, values: body.values };
    retry = (ack) => socket.emit("res:save", { ...body, ack });
    socket.emit("res:save", body);
  });
  el("res_dup")?.addEventListener("click", () => {
    if (!shown) return;
    show(shown.schema, readValues(shown.schema), null, false, shown.options, []);
  });
  el("res_delete")?.addEventListener("click", () => {
    if (!current || !current.id) return;
    lastSave = null;
    const body = {
      resource: current.key,
      id: current.id,
      expectedIdentity: current.identity || ""
    };
    retry = (ack) => socket.emit("res:remove", { ...body, ack });
    socket.emit("res:remove", body);
  });
  document.querySelectorAll('[data-modal-close="resModal"]').forEach((b) => b.addEventListener("click", close));
  el("resModal")?.addEventListener("click", (e) => {
    if (e.target === el("resModal")) close();
  });
}

// web/src/gen/areas.ts
var AREAS = [
  {
    key: "ip-pools",
    title: "IP Pools",
    navGroup: "ipsvc",
    tables: [
      { resource: "ipPool", title: "IP Pool", columns: ["name", "ranges", "used", "total", "nextPool", "comment"] }
    ]
  },
  {
    key: "address-lists",
    title: "Address Lists",
    navGroup: "security",
    tables: [
      { resource: "addressList", title: "Address List Entry", columns: ["list", "address", "timeout", "dynamic", "comment"] }
    ]
  },
  {
    key: "interface-lists",
    title: "Interface Lists",
    navGroup: "network",
    tables: [
      { resource: "ifListMember", title: "Members", columns: ["list", "interface", "disabled", "dynamic", "comment"] },
      { resource: "ifList", title: "Lists", columns: ["name", "include", "exclude", "builtin", "comment"] }
    ]
  },
  {
    key: "ip-services",
    title: "IP Services",
    navGroup: "ipsvc",
    tables: [
      { resource: "ipService", title: "IP Service", columns: ["name", "port", "proto", "availableFrom", "disabled", "dynamic", "remote"] }
    ]
  },
  {
    key: "certificates",
    title: "Certificates",
    navGroup: "security",
    tables: [
      { resource: "certificate", title: "Certificate", columns: ["name", "commonName", "privateKey", "trusted", "invalidAfter", "expiresAfter"] }
    ]
  },
  {
    key: "scripts",
    title: "Scripts",
    navGroup: "system",
    tables: [
      { resource: "script", title: "Script", columns: ["name", "owner", "policy", "runCount", "lastStarted", "comment"] }
    ]
  },
  {
    key: "scheduler",
    title: "Scheduler",
    navGroup: "system",
    tables: [
      { resource: "scheduler", title: "Scheduled Task", columns: ["name", "startTime", "interval", "nextRun", "runCount", "disabled", "comment"] }
    ]
  },
  {
    key: "ntp-client",
    title: "NTP Client",
    navGroup: "system",
    tables: [
      { resource: "ntpClient", title: "Settings", columns: ["enabled", "mode", "servers", "vrf", "status", "syncedServer", "systemOffset"] },
      { resource: "ntpServer", title: "Servers", columns: ["address", "iburst", "minPoll", "maxPoll", "disabled", "comment"] }
    ]
  },
  {
    key: "ip-addresses",
    title: "IP Addresses",
    navGroup: "network",
    tables: [
      { resource: "ipAddress", title: "IPv4", columns: ["address", "network", "interface", "disabled", "dynamic", "invalid", "comment"] },
      { resource: "ipv6Address", title: "IPv6", columns: ["address", "interface", "advertise", "disabled", "dynamic", "invalid", "comment"] }
    ]
  }
];
var AREA_KEY_SET = new Set(AREAS.map((a) => a.key));

// web/src/pages/area.ts
var activeTab = {};
var latest = {};
var writable = {};
function tabIndex(area) {
  const at = activeTab[area.key] || 0;
  return at < area.tables.length ? at : 0;
}
function cell(v) {
  return v === void 0 || v === "" ? '<span style="color:var(--text-muted)">&mdash;</span>' : esc(v);
}
function renderTabs(area) {
  const host = el("areaTabs-" + area.key);
  if (!host) return;
  if (area.tables.length < 2) {
    host.innerHTML = "";
    return;
  }
  const at = tabIndex(area);
  host.innerHTML = area.tables.map((t, i) => '<button class="stab' + (i === at ? " active" : "") + '" type="button" role="tab" aria-selected="' + (i === at ? "true" : "false") + '" data-areatab="' + esc(area.key) + '" data-areatabindex="' + i + '">' + esc(t.title) + "</button>").join("");
}
function syncAddSlot(area) {
  const slot = el("areaAdd-" + area.key);
  if (!slot) return;
  slot.setAttribute("data-res-add", area.tables[tabIndex(area)].resource);
  document.dispatchEvent(new CustomEvent("mikrodash:resmount"));
}
function render(area) {
  const body = el("areaBody-" + area.key);
  if (!body) return;
  const at = tabIndex(area);
  const declared = area.tables[at];
  const payload = latest[area.key];
  const table = payload?.tables?.[at];
  const badge = el("areaBadge-" + area.key);
  if (badge) badge.textContent = String(table?.rows?.length ?? 0);
  renderTabs(area);
  if (!payload) {
    body.innerHTML = '<div class="empty-state">Waiting&hellip;</div>';
    return;
  }
  if (payload.denied) {
    body.innerHTML = '<div class="empty-state">This router\u2019s MikroDash account cannot read ' + esc(declared.resource) + ". RouterOS requires a user group with permission for this menu.</div>";
    return;
  }
  if (table?.unsupported) {
    body.innerHTML = '<div class="empty-state">This router does not have that menu. It may need a package this RouterOS build does not include.</div>';
    return;
  }
  if (false) {
    const r = table.rows?.[0];
    body.innerHTML = r ? '<table class="table table-vcenter mb-0"><tbody data-res-rows="' + esc(declared.resource) + '">' + declared.columns.map((c) => "<tr" + resRow(r.id, r.identity, declared.resource) + '><th style="width:34%;font-weight:500;color:var(--text-muted)">' + esc(columnLabel(c)) + "</th><td>" + cell(r.values?.[c]) + "</td></tr>").join("") + "</tbody></table>" : '<div class="empty-state">The router did not return these settings.</div>';
    syncAddSlot(area);
    return;
  }
  const head = declared.columns.map((c) => "<th>" + esc(columnLabel(c)) + "</th>").join("");
  const rows = (table?.rows || []).map((r) => "<tr" + (r.values?.disabled === "true" || r.values?.invalid === "true" ? ' style="opacity:.55"' : "") + resRow(r.id, r.identity, declared.resource) + ">" + declared.columns.map((c) => "<td>" + cell(r.values?.[c]) + "</td>").join("") + "</tr>").join("");
  body.innerHTML = '<table class="table table-vcenter mb-0"><thead><tr>' + head + '</tr></thead><tbody data-res-rows="' + esc(declared.resource) + '">' + (rows || '<tr><td colspan="' + declared.columns.length + '" class="empty-state">Nothing here yet.' + (writable[declared.resource] ? " Use <strong>Add</strong> to create one." : "") + "</td></tr>") + "</tbody></table>";
  syncAddSlot(area);
}
function columnLabel(name) {
  const spaced = name.replace(/([a-z0-9])([A-Z])/g, "$1 $2");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
function initAreaPages(socket, isVisible) {
  if (AREAS.length === 0) return;
  mountAdds(socket);
  mountRows(socket);
  document.addEventListener("click", (ev) => {
    const b = ev.target?.closest?.("[data-areatab]");
    if (!b) return;
    const key = b.getAttribute("data-areatab") || "";
    const area = AREAS.find((a) => a.key === key);
    if (!area) return;
    activeTab[key] = Number(b.getAttribute("data-areatabindex") || 0);
    render(area);
  });
  socket.on("area:update", (d) => {
    if (!d || !d.area) return;
    const area = AREAS.find((a) => a.key === d.area);
    if (!area) return;
    latest[d.area] = d;
    if (isVisible(d.area)) render(area);
  });
  socket.on("res:schema", (d) => {
    if (!d || !d.key) return;
    const owns = AREAS.some((a) => a.tables.some((t) => t.resource === d.key));
    if (!owns) return;
    writable[d.key] = !!d.permitted;
    for (const area of AREAS) {
      if (isVisible(area.key)) render(area);
    }
  });
  document.addEventListener("mikrodash:pagechange", (e) => {
    const key = e.detail;
    const area = AREAS.find((a) => a.key === key);
    if (!area) return;
    render(area);
    syncAddSlot(area);
  });
}
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  AREAS,
  initAreaPages
});
