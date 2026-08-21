// harness.mjs drives goldsetui.html's OWN script -- the file the tool embeds, read from
// disk, never a copy -- against a DOM small enough to state what the page must do and
// honest enough to say when it stops doing it. It exists because the two things this
// page must never get wrong are behaviour, not text: the frame must never show one
// row's page under another row's question (ADR-0043), and a refusal must sit beside the
// field it is about and die with the decision it belonged to.
//
// Run: node harness.mjs <path to goldsetui.html>   (exit 0 = every scenario held)
import { readFileSync } from "node:fs";
import vm from "node:vm";

const htmlPath = process.argv[2];
if (!htmlPath) {
  console.error("usage: node harness.mjs <path to goldsetui.html>");
  process.exit(2);
}
const html = readFileSync(htmlPath, "utf8");

// ---------------------------------------------------------------- the DOM, in miniature

class ClassList {
  constructor(el) { this.el = el; }
  set() { return new Set((this.el.attrs.class || "").split(/\s+/).filter(Boolean)); }
  save(s) { this.el.attrs.class = [...s].join(" "); }
  add(...c) { const s = this.set(); c.forEach((x) => s.add(x)); this.save(s); }
  remove(...c) { const s = this.set(); c.forEach((x) => s.delete(x)); this.save(s); }
  contains(c) { return this.set().has(c); }
  toggle(c, force) {
    const s = this.set();
    const want = force === undefined ? !s.has(c) : !!force;
    if (want) s.add(c); else s.delete(c);
    this.save(s);
    return want;
  }
}

let nextSerial = 1;

class El {
  constructor(tag, attrs = {}) {
    this.tagName = String(tag).toUpperCase();
    this.attrs = { ...attrs };
    this.childNodes = [];
    this.parentNode = null;
    this.style = {};
    this.value = "";
    this.onload = null;
    this.listeners = {};
    this.serial = nextSerial++;
    this.classList = new ClassList(this);
  }
  get id() { return this.attrs.id || ""; }
  get className() { return this.attrs.class || ""; }
  set className(v) { this.attrs.class = String(v); }
  get textContent() {
    if (this.childNodes.length) return this.childNodes.map((c) => c.textContent).join("");
    return this.text || "";
  }
  set textContent(v) { this.text = String(v); this.childNodes = []; }
  get src() { return this.attrs.src === undefined ? "" : this.attrs.src; }
  set src(v) { this.attrs.src = String(v); }
  get href() { return this.attrs.href === undefined ? "" : this.attrs.href; }
  set href(v) { this.attrs.href = String(v); }
  setAttribute(n, v) { this.attrs[n] = String(v); }
  getAttribute(n) { return n in this.attrs ? this.attrs[n] : null; }
  removeAttribute(n) { delete this.attrs[n]; }
  appendChild(c) { c.parentNode = this; this.childNodes.push(c); return c; }
  remove() {
    if (!this.parentNode) return;
    const i = this.parentNode.childNodes.indexOf(this);
    if (i >= 0) this.parentNode.childNodes.splice(i, 1);
    this.parentNode = null;
  }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  focus() { this.doc.activeElement = this; }
  blur() { if (this.doc.activeElement === this) this.doc.activeElement = null; }
  // load is what a browser fires when the frame's new document has arrived. Nothing
  // fires it on its own here: a scenario says when, which is the whole point.
  load() {
    const ev = { type: "load", target: this };
    if (this.onload) this.onload(ev);
    (this.listeners.load || []).forEach((fn) => fn(ev));
  }
}

// makeDocument seeds the element registry from the page's own markup, so every id and
// every starting class -- `hidden` above all -- is the file's, not the harness's.
function makeDocument() {
  const doc = {
    activeElement: null,
    byId: new Map(),
    listeners: {},
    getElementById(id) { return doc.byId.get(id) || null; },
    createElement(tag) { return attach(new El(tag)); },
    createTextNode(t) { const e = attach(new El("#text")); e.textContent = t; return e; },
    addEventListener(type, fn) { (doc.listeners[type] = doc.listeners[type] || []).push(fn); },
  };
  // attach gives an element the two document-aware operations the page uses on the
  // rendering frame. A clone is attached but NOT registered: like a real one, it is not
  // findable by id until it is in the document.
  const attach = (el) => {
    el.doc = doc;
    el.cloneNode = () => attach(new El(el.tagName, el.attrs));
    el.replaceWith = (next) => {
      const p = el.parentNode;
      if (p) {
        const i = p.childNodes.indexOf(el);
        if (i >= 0) p.childNodes[i] = next;
        next.parentNode = p;
      }
      // The id goes with the element that is now in the document, or the page would
      // keep finding the one it just discarded.
      if (next.id) doc.byId.set(next.id, next);
      el.detached = true;
    };
    return el;
  };
  const adopt = (el) => {
    attach(el);
    if (el.id) doc.byId.set(el.id, el);
    return el;
  };
  const markup = html
    .replace(/<script>[\s\S]*?<\/script>/g, "")
    .replace(/<style>[\s\S]*?<\/style>/g, "");
  for (const m of markup.matchAll(/<([a-zA-Z][a-zA-Z0-9]*)\b([^>]*)>/g)) {
    const id = /(?:^|\s)id="([^"]*)"/.exec(m[2]);
    if (!id) continue;
    const cls = /(?:^|\s)class="([^"]*)"/.exec(m[2]);
    adopt(new El(m[1], { id: id[1], class: cls ? cls[1] : "" }));
  }
  return doc;
}

// ---------------------------------------------------------------- the page under test

const SCRIPT = (() => {
  const m = /<script>([\s\S]*?)<\/script>/.exec(html);
  if (!m) throw new Error("goldsetui.html carries no script block");
  return m[1];
})();

const TOKEN = "TESTTOKEN";

// newPage runs the page's script over a fresh document with a fresh responder, and
// hands back the handles a scenario drives it by.
async function newPage(responder) {
  const doc = makeDocument();
  const opened = [];
  const sandbox = {
    document: doc,
    window: { open: (...a) => opened.push(a) },
    location: { search: "?t=" + TOKEN },
    console,
    URLSearchParams,
    setTimeout: (fn, ms) => { const t = setTimeout(fn, ms); t.unref?.(); return t; },
    clearTimeout,
    async fetch(path, opts) {
      const r = await responder(path, opts);
      return { ok: r.status < 400, status: r.status, statusText: "", json: async () => r.body };
    },
  };
  const ctx = vm.createContext(sandbox);
  vm.runInContext(
    SCRIPT + "\n;globalThis.__page = { next: doNext, note: doNote, undo: doUndo, answer: doAnswer, mode: () => mode, view: () => view };",
    ctx,
    { filename: "goldsetui.html" },
  );
  // An element the page is asserted on and does not have is a finding, not a crash:
  // the whole point of a scenario is to name what is missing.
  const el = (id) => {
    const found = doc.getElementById(id);
    if (!found) throw new Error("the page carries no #" + id);
    return found;
  };
  const page = {
    doc,
    opened,
    el,
    hidden: (id) => el(id).classList.contains("hidden"),
    text: (id) => el(id).textContent,
    ...sandbox.__page,
  };
  await settle();
  return page;
}

// settle lets the page's own promise chains run to their end. Its timer is NOT
// unref'd: it is the only thing holding the loop open between two scenarios.
function settle() {
  return new Promise((r) => setTimeout(r, 0));
}

// ---------------------------------------------------------------- fixtures

const SESSION = {
  by: "A Human", dir: "/tmp/gold", stratum: "", flush_every: 5, note_runes: 120,
  question: "Is this page one job listing?",
  rubric: [
    { key: "d", label: "detail", text: "one posting" },
    { key: "h", label: "hub_index", text: "a list of postings" },
    { key: "r", label: "residue", text: "not a posting" },
    { key: "a", label: "ambiguous", text: "it cannot be told" },
  ],
  authority: "the captured content is the authority",
  refetch: true,
};

function rowView(id, opts = {}) {
  return {
    done: false,
    progress: { position: 1, total: 3, decided: 0, buffered: 0, flushed: 0, agreed: 0, comparable: 0 },
    question: {
      id,
      url: "https://acme.test/jobs/" + id,
      title: "role " + id,
      head: "captured text of " + id,
      mid: "",
      urls_total: 3, urls_same_host: 2, urls_joblike: 1,
      fidelity: { state: opts.warn ? "drifted" : "same", reason: "measured" },
      rendering: { available: opts.rendering !== false, live: true, warn: !!opts.warn },
    },
    reveal: opts.reveal || null,
    pending: opts.pending || null,
  };
}

const NOTE_ROW = rowView("rowN", {
  reveal: {
    proposed_label: "hub_index", proposed_by: "llm:x", proposer_note: "",
    current_label: "hub_index", stratum: "random", verdict: true,
    chosen: "detail", agreed: false, comparable: true, note_required: true,
  },
  pending: { label: "detail" },
});

// ---------------------------------------------------------------- the scenarios

let failures = 0;
function check(name, ok, detail) {
  if (ok) { console.log("  ok   " + name); return; }
  failures++;
  console.log("  FAIL " + name + (detail ? " -- " + detail : ""));
}

// A frame that is merely re-pointed keeps the previous document on screen until the
// next one paints. This is the scenario that would corrupt a label: row A is a single
// posting, row B is a hub index, and the labeller answers what they can see.
async function scenarioFrameNeverOutlivesItsRow() {
  console.log("the frame never shows a row the question has moved past");
  const nexts = [rowView("rowA"), rowView("rowB", { warn: true })];
  const page = await newPage(async (path) => {
    if (path === "/api/session") return { status: 200, body: SESSION };
    if (path === "/api/next") return { status: 200, body: nexts.shift() };
    throw new Error("unexpected request " + path);
  });

  const frameA = page.el("renderframe");
  check("row A points the frame at its own rendering",
    frameA.getAttribute("src") === "/render/rowA?t=" + TOKEN, frameA.getAttribute("src"));
  check("row A is covered until its rendering has loaded", !page.hidden("frameveil"));
  frameA.load();
  check("the cover lifts when row A's rendering has loaded", page.hidden("frameveil"));

  const staleLoad = frameA.onload;
  const advancing = page.next();
  check("the frame is blanked explicitly while the next row is fetched",
    page.el("renderframe").getAttribute("src") === "about:blank",
    String(page.el("renderframe").getAttribute("src")));
  await advancing;
  await settle();

  const frameB = page.el("renderframe");
  check("row B is on screen", page.text("rowid") === "rowB", page.text("rowid"));
  check("row B says the page changed since capture", !page.hidden("renderwarn"));
  check("row B replaced the frame element", frameB.serial !== frameA.serial);
  check("row B points the frame at its own rendering",
    frameB.getAttribute("src") === "/render/rowB?t=" + TOKEN, frameB.getAttribute("src"));
  check("row B's question is NOT beside row A's page", !page.hidden("frameveil"));

  staleLoad();
  check("a load belonging to row A does not uncover row B", !page.hidden("frameveil"));
  frameB.load();
  check("the cover lifts when row B's own rendering has loaded", page.hidden("frameveil"));
}

// A row with no rendering must leave nothing behind it either.
async function scenarioRowWithoutARendering() {
  console.log("a row with no rendering blanks the frame and hides the section");
  const page = await newPage(async (path) => {
    if (path === "/api/session") return { status: 200, body: SESSION };
    if (path === "/api/next") return { status: 200, body: rowView("solo", { rendering: false }) };
    throw new Error("unexpected request " + path);
  });
  check("the rendering section is hidden", page.hidden("rendering"));
  check("the captured text takes the whole column", page.el("views").classList.contains("solo"));
  check("the frame is explicitly blank",
    page.el("renderframe").getAttribute("src") === "about:blank",
    String(page.el("renderframe").getAttribute("src")));
  check("the blank frame stays covered", !page.hidden("frameveil"));
}

// A refusal the labeller can fix belongs beside the field they are typing in, and it
// belongs to ONE decision: undo takes the decision away, so it takes the refusal too.
async function scenarioNoteRefusal() {
  console.log("a refused note is stated beside the field and dies with its decision");
  let noteStatus = 400;
  let noteError = "a note is required here and cannot be blank";
  const page = await newPage(async (path) => {
    if (path === "/api/session") return { status: 200, body: SESSION };
    if (path === "/api/next") return { status: 200, body: NOTE_ROW };
    if (path === "/api/note") return { status: noteStatus, body: { error: noteError } };
    if (path === "/api/undo") return { status: 200, body: rowView("rowAfterUndo") };
    throw new Error("unexpected request " + path);
  });

  check("the note field is up", !page.hidden("notewrap"));
  check("the page is waiting for a note", page.mode() === "note", page.mode());
  check("no refusal is shown before one is refused", page.hidden("noteerror"));

  await page.note();
  await settle();
  check("the refusal is beside the note field",
    !page.hidden("noteerror") && page.text("noteerror") === noteError, page.text("noteerror"));
  check("a refusal the labeller can fix does not also shout from the corner",
    page.el("toast").childNodes.length === 0);
  check("the row is still the row that was refused", page.text("rowid") === "rowN");

  await page.undo();
  await settle();
  check("undo takes the refusal with the decision it belonged to",
    page.hidden("noteerror") && page.text("noteerror") === "", page.text("noteerror"));

  // A fault is not the labeller's to fix: it is stated beside the field AND toasted.
  noteStatus = 500;
  noteError = "the journal will not sync";
  const faulted = await newPage(async (path) => {
    if (path === "/api/session") return { status: 200, body: SESSION };
    if (path === "/api/next") return { status: 200, body: NOTE_ROW };
    if (path === "/api/note") return { status: 500, body: { error: "the journal will not sync" } };
    throw new Error("unexpected request " + path);
  });
  await faulted.note();
  await settle();
  check("a fault is stated beside the field", !faulted.hidden("noteerror"));
  check("a fault also raises the sticky toast", faulted.el("toast").childNodes.length === 1,
    String(faulted.el("toast").childNodes.length));
}

// A note over the ceiling never reaches the tool, and that refusal is beside the field too.
async function scenarioNoteCeiling() {
  console.log("a note over the ceiling is refused beside the field, without a request");
  let asked = 0;
  const page = await newPage(async (path) => {
    if (path === "/api/session") return { status: 200, body: SESSION };
    if (path === "/api/next") return { status: 200, body: NOTE_ROW };
    if (path === "/api/note") { asked++; return { status: 200, body: rowView("never") }; }
    throw new Error("unexpected request " + path);
  });
  page.el("note").value = "x".repeat(SESSION.note_runes + 1);
  await page.note();
  await settle();
  check("the ceiling refusal is beside the field", !page.hidden("noteerror"));
  check("the ceiling refusal names the ceiling",
    page.text("noteerror").includes(String(SESSION.note_runes)), page.text("noteerror"));
  check("nothing was sent", asked === 0);
}

for (const scenario of [
  scenarioFrameNeverOutlivesItsRow,
  scenarioRowWithoutARendering,
  scenarioNoteRefusal,
  scenarioNoteCeiling,
]) {
  try {
    await scenario();
  } catch (err) {
    check(scenario.name + " ran to the end", false, err.message);
  }
}
if (failures > 0) {
  console.log(failures + " assertion(s) failed");
  process.exit(1);
}
console.log("every scenario held");
