// jhh's dashboard: a window onto the data your agent works with. It reads
// and changes everything through jhh's JSON API and has no logic of its own.
//
// Text from job listings is untrusted. This file builds every element with
// createElement and textContent (the h helper) and never parses HTML, so a
// listing can't add markup or script. Only http and https links are made.
// Times arrive and leave as UTC; they're shown in the browser's time zone.
"use strict";

const STATUS = {
  new: "New", filtered_out: "Filtered out", unscreened: "Waiting to screen", screened_out: "Screened out",
  review: "To review", skipped: "Skipped", applied: "Applied", interviewing: "Interviewing", offer: "Offer",
  rejected: "Rejected", withdrawn: "Withdrawn",
};
const PIPELINE = ["applied", "interviewing", "offer", "rejected", "withdrawn", "skipped", "review"];
const FIT = { strong: "Strong fit", good: "Good fit", stretch: "Stretch", no: "Not a fit" };
const CHANNELS = ["LinkedIn connection", "LinkedIn message", "Email", "Referral ask", "Call", "Recruiter reply"];
const OUT_STATUS = { sent: "Sent", accepted: "Accepted", replied: "Replied", scheduled: "Scheduled", no_reply: "No reply", closed: "Closed" };
const FLAG_KIND = {
  canary: "This posting checks for AI-written applications",
  hostile: "Instructions aimed at an AI",
  hidden_text: "Text hidden from human readers",
  unclear: "Text addressed to an AI",
};
const TABS = [["review", "To review"], ["applied", "Applied"], ["outreach", "Outreach"], ["archive", "Archive"], ["profile", "Profile"]];
const ARCHIVE = [["archive", "Skipped and closed"], ["screened_out", "Screened out"], ["filtered_out", "Filtered out"]];
const EVENTS = {
  job_added: "Found", posting_linked: "Another posting joined this job", screened: "Rated", applied_at_set: "Applied date set",
  applied_at_cleared: "Applied date cleared", notes_changed: "Notes changed", outreach_logged: "Outreach logged",
  outreach_updated: "Outreach updated", injection_reported: "Text aimed at an AI reported", content_flag_reviewed: "Content flag reviewed",
  job_merged: "Merged into another job", jobs_merged_in: "Other jobs merged in", job_unmerged: "Merge undone",
  job_split: "Postings split off", job_split_off: "Split off from another job", posting_closed: "Posting closed",
};

const state = {
  signedOut: false, error: "", loaded: false,
  tab: "review", sub: "archive", fit: "all",
  info: null, lists: {}, outreach: null, profile: null,
  details: new Map(), open: new Set(),
  selected: 0, picked: new Set(),
  form: null, formError: "", outreachFor: null,
  visited: null, askApplied: null,
  pending: false, undo: null, live: false,
};

/* ---------- helpers ---------- */

// h builds an element. Strings become text nodes, never markup.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else if (k === "checked") el.checked = true;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid === null || kid === undefined || kid === false || kid === "") continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

// safeURL returns url if it is http or https, else "".
function safeURL(url) {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : "";
  } catch { return ""; }
}

const pad = (n) => String(n).padStart(2, "0");
const localDay = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const dayOf = (iso) => iso ? localDay(new Date(iso)) : "";
const today = () => localDay(new Date());
// dayToUTC turns a date picked in the browser into UTC: now for today, and
// noon local time for any other day.
function dayToUTC(day) {
  if (day === today()) return new Date().toISOString();
  const [y, m, d] = day.split("-").map(Number);
  return new Date(y, m - 1, d, 12).toISOString();
}
const plusDays = (n) => { const d = new Date(); d.setDate(d.getDate() + n); return localDay(d); };
const daysSince = (iso) => Math.floor((Date.now() - new Date(iso).getTime()) / 86400000);
function fmtDate(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  const opts = { month: "short", day: "numeric" };
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = "numeric";
  return d.toLocaleDateString(undefined, opts);
}
function who(actor) {
  if (!actor || actor === "user") return "you";
  if (actor === "system") return "jhh";
  return actor.startsWith("agent:") ? actor.slice(6) : actor;
}
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const $ = (sel) => document.querySelector(sel);

/* ---------- API ---------- */

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try { data = await res.json(); } catch { /* no body */ }
  if (res.status === 401) {
    state.signedOut = true;
    render();
    throw new Error("Signed out.");
  }
  if (!res.ok) throw new Error((data && data.error) || `jhh answered ${res.status}.`);
  return data;
}

function listsFor(tab) {
  switch (tab) {
    case "review": return ["review"];
    case "applied": return ["applied"];
    case "archive": return [state.sub];
    case "outreach": return ["review", "applied"];
    default: return [];
  }
}

async function loadList(name) {
  state.lists[name] = (await api("GET", `/api/jobs?list=${name}`)).jobs;
}

async function loadDetail(id) {
  try {
    state.details.set(id, await api("GET", `/api/jobs/${id}`));
  } catch (e) {
    state.details.delete(id);
    state.open.delete(id);
  }
}

// load fetches what the current tab shows. Live updates call it too.
async function load() {
  try {
    state.info = await api("GET", "/api/state");
    const work = listsFor(state.tab).map(loadList);
    if (state.tab === "outreach") work.push(api("GET", "/api/outreach").then((d) => { state.outreach = d; }));
    if (state.tab === "profile") work.push(api("GET", "/api/profile").then((d) => { state.profile = d; }));
    for (const id of state.open) work.push(loadDetail(id));
    await Promise.all(work);
    state.loaded = true;
    state.error = "";
    connect();
  } catch (e) {
    if (!state.signedOut) state.error = e.message;
  }
  safeRender();
}

let reloadTimer = null;
function reload() {
  clearTimeout(reloadTimer);
  reloadTimer = setTimeout(load, 150);
}

// connect listens for "something changed" from jhh: the agent or another
// tab saved something, so fetch again.
let events = null;
function connect() {
  if (events) return;
  events = new EventSource("/api/events");
  events.addEventListener("change", reload);
  events.onopen = () => { state.live = true; renderLive(); };
  events.onerror = () => {
    state.live = false;
    renderLive();
    if (events.readyState === EventSource.CLOSED) {
      events = null;
      setTimeout(load, 5000); // jhh restarted, or another jhh took over
    }
  };
}

/* ---------- actions ---------- */

function toast(msg, undo) {
  const t = $("#toast");
  t.replaceChildren(h("span", null, msg));
  state.undo = undo || null;
  if (undo) t.append(h("button", { class: "btn", type: "button", onclick: runUndo }, "Undo"));
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; state.undo = null; }, undo ? 8000 : 3500);
}

async function runUndo() {
  const undo = state.undo;
  state.undo = null;
  $("#toast").hidden = true;
  if (!undo) return;
  try {
    await undo();
    toast("Undone.");
  } catch (e) { toast(e.message); }
  reload();
}

// change saves a change to one job; back undoes it.
async function change(job, patch, msg, back) {
  try {
    await api("PATCH", `/api/jobs/${job.id}`, patch);
    toast(msg, back && (() => api("PATCH", `/api/jobs/${job.id}`, back)));
  } catch (e) { toast(e.message); }
  reload();
}

const undoStatus = (j) => ({ status: j.status, ...(j.applied ? {} : { clear_applied_at: true }) });

function markApplied(j) {
  change(j, { status: "applied" }, `Marked #${j.id} applied today. Change the date under Applied.`, undoStatus(j));
}
function skip(j) {
  change(j, { status: "skipped" }, `Skipped #${j.id}. It's in Archive if you change your mind.`, { status: j.status });
}
function setStatus(j, to) {
  const patch = { status: to };
  change(j, patch, `#${j.id} is now ${STATUS[to].toLowerCase()}.`, undoStatus(j));
}

async function bulkSkip() {
  const jobs = (state.lists.review || []).filter((j) => state.picked.has(j.id));
  if (!jobs.length) return;
  try {
    await api("POST", "/api/jobs/status", { changes: jobs.map((j) => ({ id: j.id, status: "skipped" })) });
    state.picked.clear();
    toast(`Skipped ${plural(jobs.length, "job", "jobs")}.`,
      () => api("POST", "/api/jobs/status", { changes: jobs.map((j) => ({ id: j.id, status: j.status })) }));
  } catch (e) { toast(e.message); }
  reload();
}

async function reviewFlag(j, flag, action, kind) {
  try {
    await api("POST", `/api/flags/${flag.id}`, kind ? { action, kind } : { action });
    const msg = action === "hide" ? `Hid #${j.id}; it's in Archive.` : "Kept. The job stays where it is.";
    toast(msg, async () => {
      await api("POST", `/api/flags/${flag.id}`, { action: "reopen" });
      if (action === "hide" && j.status !== "skipped") await api("PATCH", `/api/jobs/${j.id}`, { status: j.status });
    });
  } catch (e) { toast(e.message); }
  reload();
}

async function sendToScreening(j, on) {
  try {
    await api("POST", `/api/jobs/${j.id}/screening`, { on });
    if (on) toast(`Sent #${j.id} to screening. Ask your agent: screen new jobs.`, () => api("POST", `/api/jobs/${j.id}/screening`, { on: false }));
    else toast(`#${j.id} is filtered out again.`);
  } catch (e) { toast(e.message); }
  reload();
}

function openPosting(j) {
  const url = safeURL(j.url);
  if (!url) return;
  window.open(url, "_blank", "noopener,noreferrer");
  noteVisit(j);
}

// noteVisit remembers a posting the user opened from To review, so coming
// back to this tab asks whether they applied.
function noteVisit(j) {
  if (j.status === "review") state.visited = { id: j.id, company: j.company, title: j.title, status: j.status, applied: j.applied };
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && state.visited) {
    state.askApplied = state.visited;
    state.visited = null;
    render();
  }
});

async function toggleDetail(j, open) {
  const id = j.id;
  const want = open === undefined ? !state.open.has(id) : open;
  if (want) {
    state.open.add(id);
    if (!state.details.has(id)) await loadDetail(id);
  } else {
    state.open.delete(id);
  }
  render();
}

async function focusNotes(j) {
  await toggleDetail(j, true);
  const el = document.getElementById(`notes-${j.id}`);
  if (el) el.focus();
}

/* ---------- render ---------- */

function render() {
  renderStrip();
  renderBanners();
  renderTabs();
  renderLive();
  const view = $("#view");
  if (state.signedOut) { view.replaceChildren(signedOutView()); return; }
  if (!state.loaded) {
    view.replaceChildren(state.error ? empty("jhh isn't answering", state.error) : empty("Loading", "Your jobs, applications and outreach will appear here."));
    return;
  }
  const views = { review: viewReview, applied: viewApplied, outreach: viewOutreach, archive: viewArchive, profile: viewProfile };
  view.replaceChildren(...[].concat(views[state.tab]()));
  const sel = document.querySelector(".row.selected");
  if (sel && state.scrollToSelected) {
    sel.scrollIntoView({ block: "nearest" });
    state.scrollToSelected = false;
  }
}

// safeRender doesn't redraw while the user is typing; it catches up when
// they leave the field.
function safeRender() {
  const a = document.activeElement;
  if (a && a.closest && a.closest("#view") && (a.tagName === "TEXTAREA" || (a.tagName === "INPUT" && a.type !== "date" && a.type !== "checkbox"))) {
    state.pending = true;
    return;
  }
  render();
}
document.addEventListener("focusout", () => {
  if (state.pending) { state.pending = false; setTimeout(safeRender, 0); }
});

function renderLive() {
  const el = $("#live");
  el.hidden = !state.loaded || state.signedOut;
  el.textContent = state.live ? "Live" : "Reconnecting";
  el.className = state.live ? "live" : "live off";
  el.title = state.live ? "Changes your agent makes show up here as they happen." : "Not connected to jhh; it will try again.";
}

function renderStrip() {
  const s = state.info && state.info.summary;
  const stats = [
    [s && s.to_review, "To review"],
    [s && s.unscreened, "Waiting to screen"],
    [s && s.applications, "Applications"],
    [s && s.applied_last_7, "Applied last 7 days"],
    [s && s.interviewing + s.offers, "Interviewing"],
    [s && s.follow_ups_due, "Follow-ups due", s && s.follow_ups_due > 0],
  ];
  $("#strip").replaceChildren(...stats.map(([n, label, alert]) =>
    h("div", { class: alert ? "stat alert" : "stat" }, h("b", null, s ? String(n) : "–"), h("span", null, label))));
}

function renderBanners() {
  const out = [];
  const s = state.info && state.info.summary;
  if (state.error && state.loaded) out.push(h("div", { class: "banner bad" }, `Couldn't reach jhh: ${state.error}`));
  if (state.askApplied && !state.signedOut) {
    const v = state.askApplied;
    out.push(h("div", { class: "banner info" },
      h("span", null, "Did you apply to ", h("b", null, v.company), ": ", titleText(v), "?"),
      h("button", { class: "btn primary", type: "button", onclick: () => { state.askApplied = null; markApplied(v); } }, "Yes, mark applied"),
      h("button", { class: "btn quiet", type: "button", onclick: () => { state.askApplied = null; render(); } }, "Not yet")));
  }
  if (s && s.open_flags > 0) {
    out.push(h("div", { class: "banner" },
      `${plural(s.open_flags, "listing has", "listings have")} text addressed to an AI waiting for your review. Look for the "AI instructions" badge.`));
  }
  if (s && s.suspect_feeds > 0) {
    out.push(h("div", { class: "banner" },
      `${plural(s.suspect_feeds, "company's job feed looks", "companies' job feeds look")} wrong, so jhh isn't closing jobs there. Ask your agent: what's new?`));
  }
  $("#banners").replaceChildren(...out);
}

function renderTabs() {
  const c = state.info ? state.info.counts : null;
  const counts = c && {
    review: c.review, applied: c.applied, outreach: state.info.outreach,
    archive: c.archive + c.screened_out + c.filtered_out, profile: "",
  };
  $("#tabs").replaceChildren(...TABS.map(([k, label]) =>
    h("button", { class: "tab", role: "tab", type: "button", "aria-selected": String(state.tab === k), onclick: () => switchTab(k) },
      label, h("span", { class: "n" }, counts ? String(counts[k]) : ""))));
}

function switchTab(tab) {
  state.tab = tab;
  // Open Archive on a part that has something in it.
  const c = state.info && state.info.counts;
  if (tab === "archive" && c && !c[state.sub]) {
    const first = ARCHIVE.find(([k]) => c[k] > 0);
    if (first) state.sub = first[0];
  }
  state.form = null;
  state.selected = 0;
  try { localStorage.setItem("jhh-tab", tab); } catch { /* storage off */ }
  history.replaceState(null, "", `#${tab}`);
  render();
  load();
}

function empty(title, body, say) {
  return h("div", { class: "empty" }, h("strong", null, title), body, say ? h("div", null, h("span", { class: "say" }, say)) : null);
}

function signedOutView() {
  return h("div", { class: "empty" },
    h("strong", null, "Open the dashboard from your agent"),
    "For your privacy, the dashboard needs a fresh sign-in link. Ask your agent, or run ", h("code", null, "jhh dashboard"), " in a terminal.",
    h("div", null, h("span", { class: "say" }, "open my dashboard")));
}

/* ---------- job rows ---------- */

function pill(cls, text, title) { return h("span", { class: `pill ${cls}`, title }, text); }

function badges(j) {
  const out = [];
  if (j.fit) out.push(pill(`fit-${j.fit}`, FIT[j.fit] || j.fit, j.screened_by ? `Rated by ${who(j.screened_by)}` : null));
  if (j.status !== "review" && j.status !== "unscreened") {
    const cls = j.status === "applied" ? "st-applied" : ["interviewing", "offer"].includes(j.status) ? "st-ok"
      : ["rejected", "withdrawn"].includes(j.status) ? "st-bad" : "st-idle";
    out.push(pill(cls, STATUS[j.status] || j.status));
  }
  if (j.applied_unconfirmed) out.push(pill("st-warn", "Confirm applied", "Your agent wasn't sure this was submitted. Set the applied date to confirm."));
  if (j.open_flags && j.open_flags.length) {
    const canaryOnly = j.open_flags.every((k) => k === "canary");
    out.push(pill(canaryOnly ? "st-warn" : "st-bad", canaryOnly ? "AI check" : "AI instructions", j.open_flags.map((k) => FLAG_KIND[k] || k).join("; ")));
  }
  if (j.closed) out.push(pill("st-idle", "Posting closed", `Closed ${fmtDate(j.closed)}`));
  if (j.reposted) out.push(pill("st-warn", "Reposted", `Posted again ${fmtDate(j.reposted)}`));
  if (j.company_suspect) out.push(pill("st-warn", "Suspect feed", "This company's last fetch looked wrong, so jhh isn't closing its jobs."));
  if (j.applied && j.status === "applied") out.push(h("span", { class: "mono hint" }, `${daysSince(j.applied)}d ago`));
  return out;
}

// titleText is a job's title, or a stand-in for an ad that names no role
// in its title (a Hacker News ad lists them in its text).
function titleText(j) {
  return j.title || "Roles in the post";
}

function titleEl(j) {
  const url = safeURL(j.url);
  if (!url) return h("span", { class: "title" }, titleText(j));
  return h("a", { class: "title", href: url, target: "_blank", rel: "noopener noreferrer", onclick: () => noteVisit(j) }, titleText(j));
}

function metaLine(j) {
  const where = [j.workplace && j.workplace !== "unknown" ? j.workplace[0].toUpperCase() + j.workplace.slice(1) : "", (j.places || []).join("; ")]
    .filter(Boolean).join(" · ");
  const items = [
    where ? h("span", null, where) : null,
    j.pay ? h("span", { class: "mono" }, j.pay) : null,
    j.ask ? h("span", { class: "mono", title: "A suggested ask: part of the way up the posted range (the ask_percentile setting)" }, `ask ${j.ask}`) : null,
    j.posted && ["review", "unscreened"].includes(j.status) ? h("span", { class: "mono" }, `posted ${fmtDate(j.posted)}`) : null,
    j.via ? h("span", null, `via ${j.via}`) : null,
    j.origin === "manual" ? h("span", null, "added by you") : j.origin === "agent" ? h("span", null, "added by your agent") : null,
  ].filter(Boolean);
  return items.length ? h("div", { class: "meta" }, items) : null;
}

function jobActions(j) {
  const id = j.id;
  switch (j.status) {
    case "review":
    case "unscreened":
      return [
        h("button", { class: "btn primary", type: "button", onclick: () => markApplied(j) }, "Mark applied"),
        h("button", { class: "btn", type: "button", onclick: () => logOutreachFor(j) }, "Log outreach"),
        h("button", { class: "btn quiet", type: "button", onclick: () => skip(j) }, "Skip"),
      ];
    case "filtered_out":
      return [h("button", { class: "btn", type: "button", onclick: () => sendToScreening(j, true) }, "Send to screening")];
    case "screened_out":
      return [h("button", { class: "btn", type: "button", onclick: () => setStatus(j, "review") }, "Back to review")];
    default: {
      const opts = PIPELINE.map((k) => h("option", { value: k, selected: k === j.status }, STATUS[k]));
      return [
        h("label", { class: "inline" }, "Applied",
          h("input", { type: "date", value: dayOf(j.applied), max: today(), "aria-label": `Applied date for #${id}`,
            onchange: (e) => e.target.value && change(j, { applied_at: dayToUTC(e.target.value) }, "Applied date saved.",
              j.applied ? { applied_at: j.applied } : { clear_applied_at: true }) })),
        h("select", { class: "compact", "aria-label": `Status of #${id}`, onchange: (e) => setStatus(j, e.target.value) }, opts),
        h("button", { class: "btn", type: "button", onclick: () => logOutreachFor(j) }, "Log outreach"),
      ];
    }
  }
}

function jobRow(j, opts = {}) {
  const selected = opts.selected;
  const dim = ["skipped", "rejected", "withdrawn", "screened_out", "filtered_out"].includes(j.status) || j.closed;
  const head = h("div", { class: "row-head" },
    opts.pickable ? h("input", { type: "checkbox", class: "pick", checked: state.picked.has(j.id), "aria-label": `Select #${j.id}`,
      onchange: (e) => { if (e.target.checked) state.picked.add(j.id); else state.picked.delete(j.id); render(); } }) : null,
    h("span", { class: "num" }, `#${j.id}`), h("span", { class: "company" }, j.company), titleEl(j), badges(j));
  const flags = j.flags && j.flags.length ? h("p", { class: "note" }, j.flags.join(" · ")) : null;
  const reasons = j.status === "filtered_out" && j.prefilter && j.prefilter.length
    ? h("ul", { class: "reasons" }, j.prefilter.map((r) => h("li", null, r.text))) : null;
  return h("article", { class: `row${dim ? " dim" : ""}${selected ? " selected" : ""}`, dataset: { id: String(j.id) } },
    h("div", { class: "row-main" },
      head, metaLine(j),
      j.reason ? h("p", { class: "reason" }, j.reason) : null,
      j.gaps ? h("p", { class: "gaps" }, h("b", null, "Gaps: "), j.gaps) : null,
      flags, reasons,
      j.notes ? h("p", { class: "mynote" }, h("b", null, "Notes: "), j.notes) : null,
      opts.extra || null,
      detailsEl(j)),
    h("div", { class: "actions" }, jobActions(j)));
}

function detailsEl(j) {
  const open = state.open.has(j.id);
  const d = state.details.get(j.id);
  const hasFlags = j.open_flags && j.open_flags.length;
  const summary = hasFlags ? "Review the AI instructions, description and notes" : "Description, notes and history";
  const el = h("details", { class: "more", open },
    h("summary", { onclick: (e) => { e.preventDefault(); toggleDetail(j); } }, summary));
  if (!open) return el;
  if (!d) {
    el.append(h("p", { class: "hint" }, "Loading…"));
    return el;
  }
  for (const f of d.content_flags.filter((f) => f.status === "open")) el.append(flagEl(j, f));
  if (d.description) el.append(h("div", { class: "desc" }, d.description));
  else el.append(h("p", { class: "hint" }, "No description was stored for this job."));
  if (d.postings.length > 1) {
    el.append(h("span", { class: "label" }, `${d.postings.length} postings`),
      h("ul", { class: "postings" }, d.postings.map((p) => {
        const url = safeURL(p.url);
        return h("li", null, url ? h("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, titleText(p)) : titleText(p),
          p.location ? ` · ${p.location}` : "", ` · ${p.source}`, p.closed ? ` · closed ${fmtDate(p.closed)}` : "");
      })));
  }
  el.append(h("label", { class: "notes-box" }, "Your notes",
    h("textarea", { id: `notes-${j.id}`, rows: "3", placeholder: "Recruiter name, referral, what you emphasized, interview dates",
      value: j.notes || "", onchange: (e) => change(j, { notes: e.target.value.trim() }, "Notes saved.", { notes: j.notes || "" }) })));
  if (d.events.length) {
    el.append(h("span", { class: "label" }, "History"),
      h("ul", { class: "timeline" }, d.events.slice().reverse().map((e) =>
        h("li", null, h("span", { class: "when" }, fmtDate(e.at)), h("span", null, eventText(e)), h("span", { class: "who" }, who(e.actor))))));
  }
  return el;
}

function eventText(e) {
  const d = e.data || {};
  switch (e.kind) {
    case "status_changed": {
      const why = d.cause === "prefilter" ? " (filters)" : d.cause === "content_flag" ? " (hidden for AI instructions)" : d.cause === "prefilter_override" ? " (sent to screening)" : "";
      return `${STATUS[d.from] || d.from} → ${STATUS[d.to] || d.to}${why}`;
    }
    case "screened": return `Rated: ${FIT[d.fit] || d.fit}`;
    default: return EVENTS[e.kind] || e.kind.replaceAll("_", " ");
  }
}

function flagEl(j, f) {
  const canary = f.kind === "canary";
  return h("div", { class: `flag${canary ? " canary" : ""}` },
    h("b", null, FLAG_KIND[f.kind] || f.kind),
    h("span", { class: "hint" }, canary
      ? "Some companies ask AI tools to include a codeword, to spot AI-written applications. The job is shown normally."
      : `Found by ${f.detector.startsWith("rule:") ? "jhh's rules" : who(f.detector)}. Your agent rates this job on what a human reader sees.`),
    h("blockquote", null, f.excerpt),
    h("div", { class: "flag-actions" },
      h("button", { class: "btn", type: "button", onclick: () => reviewFlag(j, f, "keep") }, "Keep"),
      canary ? null : h("button", { class: "btn", type: "button", onclick: () => reviewFlag(j, f, "keep", "canary") }, "Keep: it's an AI check"),
      h("button", { class: "btn danger", type: "button", onclick: () => reviewFlag(j, f, "hide") }, "Hide job")));
}

// groups puts openings with the same title at one company together, so
// the list shows one row with "2 openings with this title".
function groups(jobs) {
  const by = new Map();
  for (const j of jobs) {
    const key = `${j.company.toLowerCase()}\u0000${j.title.toLowerCase().replace(/\s+/g, " ").trim()}`;
    if (by.has(key)) by.get(key).others.push(j);
    else by.set(key, { lead: j, others: [] });
  }
  return [...by.values()];
}

function groupRows(gs, opts = {}) {
  return gs.map((g, i) => {
    const extra = g.others.length ? h("details", { class: "group" },
      h("summary", null, `${g.others.length + 1} openings with this title`),
      h("div", { class: "list" }, g.others.map((o) => jobRow(o, { pickable: opts.pickable })))) : null;
    return jobRow(g.lead, { pickable: opts.pickable, selected: opts.keys && i === state.selected, extra });
  });
}

/* ---------- views ---------- */

function reviewGroups() {
  let jobs = state.lists.review || [];
  if (state.fit !== "all") jobs = jobs.filter((j) => j.fit === state.fit);
  return groups(jobs);
}

function viewReview() {
  const gs = reviewGroups();
  state.selected = Math.min(state.selected, Math.max(gs.length - 1, 0));
  const chips = [["all", "All"], ["strong", "Strong"], ["good", "Good"], ["stretch", "Stretch"]].map(([k, label]) =>
    h("button", { class: "chip", type: "button", "aria-pressed": String(state.fit === k), onclick: () => { state.fit = k; state.selected = 0; render(); } }, label));
  const picked = state.picked.size;
  const toolbar = h("div", { class: "toolbar" },
    h("div", { class: "chips" }, chips,
      picked ? h("button", { class: "btn", type: "button", onclick: bulkSkip }, `Skip ${picked} selected`) : null),
    h("div", { class: "chips" },
      h("span", { class: "hint keys" }, h("kbd", null, "j"), h("kbd", null, "k"), " move · ", h("kbd", null, "a"), " applied · ",
        h("kbd", null, "s"), " skip · ", h("kbd", null, "o"), " open · ", h("kbd", null, "n"), " notes · ", h("kbd", null, "x"), " select · ",
        h("kbd", null, "u"), " undo"),
      h("button", { class: "btn", type: "button", onclick: () => toggleForm("job") }, state.form === "job" ? "Close" : "Add a role")));
  const list = gs.length ? h("div", { class: "list" }, groupRows(gs, { pickable: true, keys: true })) : reviewEmpty();
  return [toolbar, state.form === "job" ? jobForm() : null, list].filter(Boolean);
}

function reviewEmpty() {
  const info = state.info;
  if (!info.setup.profile || !info.setup.filters) {
    return empty("Set up jhh with your agent", "Your agent interviews you, writes your profile and filters, and picks companies to follow. Tell it:", "initialize job hunt harness");
  }
  if (state.fit !== "all") return empty("No jobs with this rating", "Try another rating above.");
  if (info.summary.unscreened > 0) {
    return empty(`${plural(info.summary.unscreened, "job is", "jobs are")} waiting to be screened`, "Your agent rates them against your profile. Tell it:", "screen new jobs");
  }
  return empty("Nothing to review", "Ask your agent to look for new jobs, or add a role you found yourself.", "refresh and screen new jobs");
}

function viewApplied() {
  const jobs = state.lists.applied || [];
  const sections = [["interviewing", "Interviewing"], ["offer", "Offers"], ["applied", "Waiting to hear back"]].map(([s, label]) => {
    const js = jobs.filter((j) => j.status === s);
    return js.length ? [h("p", { class: "section-label" }, `${label} · ${js.length}`), js.map((j) => jobRow(j))] : [];
  }).flat(2);
  return sections.length ? h("div", { class: "list" }, sections)
    : empty("No applications yet", "Mark a role applied from To review and it moves here.");
}

function viewArchive() {
  const tabs = h("div", { class: "toolbar" }, h("div", { class: "chips" }, ARCHIVE.map(([k, label]) =>
    h("button", { class: "chip", type: "button", "aria-pressed": String(state.sub === k), onclick: () => { state.sub = k; render(); load(); } },
      label, state.info ? ` ${state.info.counts[k]}` : ""))));
  const jobs = state.lists[state.sub] || [];
  const explain = {
    archive: ["Archive is empty", "Skipped, rejected and withdrawn roles collect here."],
    screened_out: ["Nothing screened out", "Jobs your agent rated as not a fit land here, with its reason."],
    filtered_out: ["Nothing filtered out", "Jobs your filters dropped land here, with the rule that dropped them. Send one to screening if a filter got it wrong, and ask your agent to adjust the filters."],
  }[state.sub];
  const intro = state.sub === "filtered_out" && jobs.length
    ? h("p", { class: "hint" }, "If a filter dropped something you want, send it to screening and tell your agent which rule was wrong.") : null;
  return [tabs, intro, jobs.length ? h("div", { class: "list" }, jobs.map((j) => jobRow(j))) : empty(...explain)].filter(Boolean);
}

/* ---------- forms ---------- */

function toggleForm(name) {
  state.form = state.form === name ? null : name;
  state.formError = "";
  if (!state.form) state.outreachFor = null;
  render();
}

function field(label, attrs, wide) {
  return h("label", { class: wide ? "wide" : null }, label, attrs.tag === "textarea"
    ? h("textarea", { ...attrs, tag: null }) : attrs.tag === "select" ? h("select", { ...attrs, tag: null, options: null }, attrs.options) : h("input", attrs));
}

function jobForm() {
  return h("form", { class: "panel", onsubmit: submitJob },
    h("h2", null, "Add a role"),
    h("p", { class: "hint" }, "Paste a Greenhouse, Lever or Ashby posting link and jhh reads the rest from the board. For anything else, fill in what you know."),
    h("div", { class: "form-grid" },
      field("Posting link", { name: "url", type: "url", placeholder: "https://" }, true),
      field("Company", { name: "company", required: true }),
      field("Title", { name: "title" }),
      field("Location", { name: "location", placeholder: "Remote, US" }),
      field("Pay range", { name: "pay", placeholder: "$120,000 – $150,000" }),
      field("Description", { tag: "textarea", name: "description", rows: "3", placeholder: "Paste the job description" }, true),
      field("Notes", { name: "notes", placeholder: "Who referred you, where you found it" }, true)),
    state.formError ? h("p", { class: "form-error" }, state.formError) : null,
    h("div", { class: "form-actions" },
      h("button", { class: "btn quiet", type: "button", onclick: () => toggleForm("job") }, "Cancel"),
      h("button", { class: "btn primary", type: "submit" }, "Add role")));
}

async function submitJob(e) {
  e.preventDefault();
  const data = Object.fromEntries(new FormData(e.target).entries());
  const btn = e.target.querySelector("button[type=submit]");
  btn.disabled = true;
  try {
    const res = await api("POST", "/api/jobs", data);
    state.form = null;
    state.formError = "";
    toast(res.existing ? `#${res.id} was already in jhh.` : `Added #${res.id} to ${STATUS[res.status] || res.status}.`);
    reload();
  } catch (err) {
    state.formError = err.message;
    btn.disabled = false;
    render();
  }
}

function logOutreachFor(j) {
  state.outreachFor = j;
  state.tab = "outreach";
  state.form = "outreach";
  state.formError = "";
  render();
  load().then(() => { const el = document.querySelector("input[name=person]"); if (el) el.focus(); });
}

function outreachForm() {
  const pre = state.outreachFor;
  const jobs = [...(state.lists.applied || []), ...(state.lists.review || [])];
  if (pre && !jobs.some((j) => j.id === pre.id)) jobs.unshift(pre);
  const jobOpts = [h("option", { value: "" }, "None"),
    ...jobs.map((j) => h("option", { value: String(j.id), selected: pre && pre.id === j.id }, `#${j.id} ${j.company} · ${titleText(j)}`))];
  return h("form", { class: "panel", onsubmit: submitOutreach },
    h("h2", null, "Log outreach"),
    h("div", { class: "form-grid" },
      field("Person", { name: "person", required: true }),
      field("Their role", { name: "role", placeholder: "Hiring manager, recruiter…" }),
      field("Company", { name: "company", value: pre ? pre.company : "", placeholder: "Taken from the role if empty" }),
      field("Channel", { tag: "select", name: "channel", options: CHANNELS.map((c) => h("option", { value: c.toLowerCase() }, c)) }),
      field("Sent on", { name: "sent", type: "date", value: today(), max: today() }),
      field("Follow up on", { name: "follow_up", type: "date", value: plusDays(7) }),
      field("Related role", { tag: "select", name: "job_id", options: jobOpts }, true),
      field("Note", { tag: "textarea", name: "note", rows: "2", placeholder: "What you sent or asked for" }, true)),
    state.formError ? h("p", { class: "form-error" }, state.formError) : null,
    h("div", { class: "form-actions" },
      h("button", { class: "btn quiet", type: "button", onclick: () => toggleForm("outreach") }, "Cancel"),
      h("button", { class: "btn primary", type: "submit" }, "Save")));
}

async function submitOutreach(e) {
  e.preventDefault();
  const f = Object.fromEntries(new FormData(e.target).entries());
  const body = { person: f.person, role: f.role, company: f.company, channel: f.channel, note: f.note,
    job_id: f.job_id ? Number(f.job_id) : 0, sent_at: f.sent ? dayToUTC(f.sent) : "", follow_up_at: f.follow_up ? dayToUTC(f.follow_up) : "" };
  try {
    await api("POST", "/api/outreach", body);
    state.form = null;
    state.outreachFor = null;
    state.formError = "";
    toast("Outreach logged.");
    reload();
  } catch (err) {
    state.formError = err.message;
    render();
  }
}

/* ---------- outreach ---------- */

// channelName shows a channel as the form lists it ("LinkedIn message");
// agents can log others, which are shown as they wrote them.
function channelName(c) {
  return CHANNELS.find((name) => name.toLowerCase() === c) || c[0].toUpperCase() + c.slice(1);
}

const waiting = (o) => o.status === "sent" || o.status === "accepted";
const due = (o) => waiting(o) && o.follow_up && new Date(o.follow_up) <= new Date();

async function updateOutreach(o, patch, msg, back) {
  try {
    await api("PATCH", `/api/outreach/${o.id}`, patch);
    toast(msg, back && (() => api("PATCH", `/api/outreach/${o.id}`, back)));
  } catch (e) { toast(e.message); }
  reload();
}

function outreachRow(o) {
  const cls = ["replied", "accepted", "scheduled"].includes(o.status) ? "st-ok" : ["no_reply", "closed"].includes(o.status) ? "st-idle" : "st-applied";
  const opts = (state.outreach.statuses || Object.keys(OUT_STATUS)).map((k) => h("option", { value: k, selected: k === o.status }, OUT_STATUS[k] || k));
  return h("article", { class: "row" },
    h("div", { class: "row-main" },
      h("div", { class: "row-head" }, h("span", { class: "company" }, o.person),
        h("span", null, [o.role, o.company].filter(Boolean).join(", ")),
        pill(cls, OUT_STATUS[o.status] || o.status), due(o) ? pill("st-warn", "Follow up") : null),
      h("div", { class: "meta" }, h("span", null, channelName(o.channel)), h("span", { class: "mono" }, fmtDate(o.sent)),
        o.job_id ? h("span", null, `#${o.job_id} ${o.job_title}`) : null),
      o.note ? h("p", { class: "reason mynote" }, o.note) : null),
    h("div", { class: "actions" },
      h("label", { class: "inline" }, "Follow up",
        h("input", { type: "date", value: dayOf(o.follow_up), min: dayOf(o.sent) || null, "aria-label": `Follow-up date for ${o.person}`,
          onchange: (e) => updateOutreach(o, e.target.value ? { follow_up_at: dayToUTC(e.target.value) } : { clear_follow_up: true },
            e.target.value ? "Follow-up date saved." : "Follow-up cleared.", o.follow_up ? { follow_up_at: o.follow_up } : { clear_follow_up: true }) })),
      h("select", { class: "compact", "aria-label": `Status of the message to ${o.person}`,
        onchange: (e) => updateOutreach(o, { status: e.target.value }, `Marked ${(OUT_STATUS[e.target.value] || e.target.value).toLowerCase()}.`, { status: o.status }) }, opts)));
}

function viewOutreach() {
  const items = ((state.outreach && state.outreach.items) || []).slice()
    .sort((a, b) => (due(b) - due(a)) || b.sent.localeCompare(a.sent));
  const toolbar = h("div", { class: "toolbar" }, h("span", { class: "sub" }, "Connection requests, messages, referral asks and recruiter threads."),
    h("button", { class: "btn primary", type: "button", onclick: () => toggleForm("outreach") }, state.form === "outreach" ? "Close" : "Log outreach"));
  return [toolbar, state.form === "outreach" ? outreachForm() : null,
    items.length ? h("div", { class: "list" }, items.map(outreachRow))
      : empty("No outreach logged", "Log a connection request or message and set a follow-up date, or tell your agent who you wrote to.")].filter(Boolean);
}

/* ---------- profile ---------- */

// markdownText shows markdown as text with its headings as headings. Only
// headings and paragraphs are read; everything else stays as written.
function markdownText(md) {
  const out = [];
  let para = [];
  const flush = () => {
    if (para.length) out.push(h("p", null, para.join("\n")));
    para = [];
  };
  for (const line of md.split("\n")) {
    const m = /^(#{1,6})\s+(.*)$/.exec(line);
    if (m) {
      flush();
      out.push(h(m[1].length <= 2 ? "h3" : "h4", null, m[2]));
    } else if (!line.trim()) {
      flush();
    } else {
      para.push(line);
    }
  }
  flush();
  return h("div", { class: "doc" }, out);
}

function viewProfile() {
  const p = state.profile;
  if (!p) return empty("Loading", "");
  const intro = h("div", { class: "banner info" },
    "This is what your agent has set up. To change anything, tell your agent, for example: \"add to my profile that I led the billing migration\".");
  const profile = h("section", { class: "panel" }, h("h2", null, "Profile"),
    p.profile
      ? [h("p", { class: "hint" }, `Version ${p.profile.version}, saved by ${who(p.profile.by)} on ${fmtDate(p.profile.at)}${p.profile.summary ? `: ${p.profile.summary}` : ""}`),
        markdownText(p.profile.markdown)]
      : h("p", { class: "hint" }, "No profile yet. Tell your agent: initialize job hunt harness."),
    p.resume
      ? h("p", { class: "hint" }, `Resume text on file: ${p.resume.chars.toLocaleString()} characters, ${p.resume.source === "file" ? "read from a file" : "pasted"} on ${fmtDate(p.resume.at)}.`)
      : h("p", { class: "hint" }, "No resume text saved."));
  const byKind = new Map();
  for (const m of p.memories) {
    if (!byKind.has(m.kind)) byKind.set(m.kind, []);
    byKind.get(m.kind).push(m);
  }
  const memories = h("section", { class: "panel" }, h("h2", null, `Memories · ${p.memories.length}`),
    h("p", { class: "hint" }, "What your agent learned about you. Ones marked inferred are its own conclusions, not things you said."),
    p.memories.length ? [...byKind.entries()].map(([kind, ms]) => [
      h("h3", null, kind.replaceAll("_", " ")),
      h("ul", { class: "memories" }, ms.map((m) => h("li", null, h("span", null, m.content),
        h("span", { class: "meta" }, m.basis === "inferred" ? pill("st-warn", "inferred") : "you said", ` · ${who(m.by)} · ${fmtDate(m.at)}`,
          m.evidence ? ` · ${m.evidence}` : "")))),
    ]) : h("p", { class: "hint" }, "None yet."));
  const filters = h("section", { class: "panel" }, h("h2", null, "Filters"),
    p.filters
      ? [h("p", { class: "hint" }, `Version ${p.filters.version}, set by ${who(p.filters.by)} on ${fmtDate(p.filters.at)}.`),
        p.filters.rationale ? h("p", { class: "reason" }, p.filters.rationale) : null,
        h("pre", { class: "code" }, JSON.stringify(p.filters.filters, null, 2))]
      : h("p", { class: "hint" }, "No filters yet. Your agent drafts them with you during setup."));
  return [intro, profile, memories, filters];
}

/* ---------- keyboard ---------- */

document.addEventListener("keydown", (e) => {
  if (e.defaultPrevented || e.altKey || e.metaKey) return;
  const t = e.target;
  if (t && t.closest && t.closest("input, textarea, select, [contenteditable]")) {
    if (e.key === "Escape") t.blur();
    return;
  }
  if ((e.ctrlKey && e.key === "z") || (!e.ctrlKey && e.key === "u")) {
    if (state.undo) { e.preventDefault(); runUndo(); }
    return;
  }
  if (e.ctrlKey || state.tab !== "review" || !state.loaded) return;
  const gs = reviewGroups();
  if (!gs.length) return;
  const j = gs[Math.min(state.selected, gs.length - 1)].lead;
  switch (e.key) {
    case "j": state.selected = Math.min(state.selected + 1, gs.length - 1); break;
    case "k": state.selected = Math.max(state.selected - 1, 0); break;
    case "a": markApplied(j); break;
    case "s": skip(j); break;
    case "o": openPosting(j); break;
    case "n": focusNotes(j); e.preventDefault(); return;
    case "x": if (state.picked.has(j.id)) state.picked.delete(j.id); else state.picked.add(j.id); break;
    case "Enter": toggleDetail(j); e.preventDefault(); return;
    default: return;
  }
  e.preventDefault();
  state.scrollToSelected = true;
  render();
});

/* ---------- start ---------- */

(function start() {
  let tab = location.hash.slice(1);
  if (!tab) { try { tab = localStorage.getItem("jhh-tab") || ""; } catch { /* storage off */ } }
  if (TABS.some(([k]) => k === tab)) state.tab = tab;
  render();
  load();
})();
