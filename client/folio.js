// folio client: in-place navigation, enhanced forms and lifecycle tracking.
//
// Include it with {{template "folio/head" .}} in your shell layout's <head>.
// It is a classic (non-module) script so page scripts can use the tracking
// helpers straight away.
//
// Navigation fetches the next page and swaps the element with id "content"
// (or the element marked data-folio-content). When the server knows the
// browser already shows the same shell layout it sends only the inner HTML.
// Timers, animation frames, tracked listeners and tracked observers created by
// the old page are cleaned up first. An "appLoad" event (detail: {path,
// route}) fires on document after the first load and after every navigation.
(() => {
  if (window.folio) return;

  const intervals = new Set();
  const timeouts = new Set();
  const rafs = new Set();
  const listeners = [];
  const observers = [];

  const native = {
    setInterval: window.setInterval.bind(window),
    clearInterval: window.clearInterval.bind(window),
    setTimeout: window.setTimeout.bind(window),
    clearTimeout: window.clearTimeout.bind(window),
    requestAnimationFrame: window.requestAnimationFrame.bind(window),
    cancelAnimationFrame: window.cancelAnimationFrame.bind(window),
  };

  window.setInterval = (fn, delay, ...args) => {
    const id = native.setInterval(fn, delay, ...args);
    intervals.add(id);
    return id;
  };
  window.clearInterval = (id) => {
    native.clearInterval(id);
    intervals.delete(id);
  };
  window.setTimeout = (fn, delay, ...args) => {
    const id = native.setTimeout(() => {
      timeouts.delete(id);
      typeof fn === "function" ? fn(...args) : (0, eval)(fn);
    }, delay);
    timeouts.add(id);
    return id;
  };
  window.clearTimeout = (id) => {
    native.clearTimeout(id);
    timeouts.delete(id);
  };
  window.requestAnimationFrame = (cb) => {
    const id = native.requestAnimationFrame((t) => {
      rafs.delete(id);
      cb(t);
    });
    rafs.add(id);
    return id;
  };
  window.cancelAnimationFrame = (id) => {
    native.cancelAnimationFrame(id);
    rafs.delete(id);
  };

  // Listener that is removed when the user navigates away from the page.
  function trackEventListener(target, type, handler, options) {
    target.addEventListener(type, handler, options);
    listeners.push({ target, type, handler, options });
  }

  // Observer (Mutation/Intersection/Resize) disconnected on navigation.
  function trackObserver(observer) {
    observers.push(observer);
    return observer;
  }

  function cleanup() {
    intervals.forEach((id) => native.clearInterval(id));
    intervals.clear();
    timeouts.forEach((id) => native.clearTimeout(id));
    timeouts.clear();
    rafs.forEach((id) => native.cancelAnimationFrame(id));
    rafs.clear();
    for (const { target, type, handler, options } of listeners) {
      target.removeEventListener(type, handler, options);
    }
    listeners.length = 0;
    observers.forEach((o) => o.disconnect());
    observers.length = 0;
  }

  function meta(doc, name) {
    const el = doc.querySelector(`meta[name="${name}"]`);
    return el ? el.getAttribute("content") : null;
  }

  function routeFromPath(path) {
    const r = path.replace(/^\/+|\/+$/g, "");
    return r === "" ? "index" : r;
  }

  const state = {
    route: meta(document, "folio-route") ?? routeFromPath(location.pathname),
    shell: meta(document, "folio-shell"),
    loaded: false,
  };

  function contentElement(root) {
    return root.querySelector("[data-folio-content]") || root.getElementById("content");
  }

  function emitLoad() {
    state.loaded = true;
    document.dispatchEvent(
      new CustomEvent("appLoad", {
        detail: { timestamp: Date.now(), path: location.pathname, route: state.route },
      }),
    );
  }

  // Inserted <script> elements only run if they are freshly created.
  function runScripts(container) {
    for (const old of container.querySelectorAll("script")) {
      const s = document.createElement("script");
      for (const { name, value } of old.attributes) s.setAttribute(name, value);
      s.async = false;
      s.textContent = old.textContent;
      old.replaceWith(s);
    }
  }

  // Replaces the whole document, e.g. when a form post lands on a page with
  // a different shell. Scripts in it run as usual.
  function replaceDocument(html) {
    cleanup();
    document.open();
    document.write(html);
    document.close();
  }

  function scrollAfter(url) {
    const hash = new URL(url, location.href).hash;
    const target = hash && document.getElementById(decodeURIComponent(hash.slice(1)));
    if (target) target.scrollIntoView();
    else window.scrollTo(0, 0);
  }

  let generation = 0;

  // load fetches url and puts the result on screen. Returns false if the
  // browser had to fall back to a full page load.
  async function load(url, { method = "GET", body, history: mode = "push", scroll = true } = {}) {
    const current = ++generation;
    const headers = { "X-Folio-Navigate": "1" };
    if (state.shell) headers["X-Folio-Shell"] = state.shell;

    let response, html;
    try {
      response = await fetch(url, { method, body, headers, credentials: "same-origin" });
      html = await response.text();
    } catch (err) {
      console.error("[folio] navigation failed:", err);
      if (method === "GET") location.assign(url);
      return false;
    }
    if (current !== generation) return true; // a newer navigation won

    const finalURL = response.redirected ? response.url : url;
    const container = contentElement(document);
    const partial = response.headers.get("X-Folio-Partial") === "1";
    let content, title;

    if (partial && !container) {
      location.assign(finalURL);
      return false;
    }
    if (partial) {
      content = html;
      const t = response.headers.get("X-Folio-Title");
      if (t !== null) title = decodeURIComponent(t);
    } else {
      const doc = new DOMParser().parseFromString(html, "text/html");
      const next = contentElement(doc);
      const sameShell = meta(doc, "folio-shell") === state.shell;
      if (!next || !container || !sameShell) {
        // A different layout or an error page: show it as a real page.
        if (method === "GET") {
          location.assign(finalURL);
        } else {
          if (mode !== "none") history.pushState(null, "", finalURL);
          replaceDocument(html);
        }
        return false;
      }
      content = next.innerHTML;
      title = doc.title;
      state.shell = meta(doc, "folio-shell");
    }

    const newURL = new URL(finalURL, location.href);
    if (mode === "push" && newURL.href !== location.href) history.pushState(null, "", newURL);
    else if (mode === "replace") history.replaceState(null, "", newURL);

    cleanup();
    state.route = response.headers.get("X-Folio-Route") || routeFromPath(newURL.pathname);
    if (title !== undefined) document.title = title;
    container.innerHTML = content;
    runScripts(container);
    if (scroll) scrollAfter(newURL.href);
    emitLoad();
    return true;
  }

  function sameOrigin(url) {
    return new URL(url, location.href).origin === location.origin;
  }

  // Navigate to url without a full page load.
  function navigate(url, { replace = false } = {}) {
    if (!sameOrigin(url)) {
      location.assign(url);
      return Promise.resolve(false);
    }
    return load(url, { history: replace ? "replace" : "push" });
  }

  // The URL a form submits to. A button's formAction property falls back to
  // the page URL (not the form's action) when it has no formaction attribute,
  // so only use it when the attribute is there.
  function actionOf(form, submitter) {
    if (submitter && submitter.hasAttribute("formaction")) return submitter.formAction;
    return form.action || location.href;
  }

  // Submit a form in place. GET forms navigate; POST forms post and show the
  // response, following redirects.
  function submit(form, submitter) {
    const method = ((submitter && submitter.getAttribute("formmethod")) || form.getAttribute("method") || "GET").toUpperCase();
    const action = actionOf(form, submitter);
    const data = new FormData(form, submitter || undefined);
    if (method === "GET") {
      const url = new URL(action, location.href);
      url.search = new URLSearchParams(data).toString();
      return navigate(url.href);
    }
    const enctype = (submitter && submitter.getAttribute("formenctype")) || form.enctype;
    const body = enctype === "multipart/form-data" ? data : new URLSearchParams(data);
    return load(action, { method, body });
  }

  window.addEventListener("popstate", () => load(location.href, { history: "none", scroll: false }));

  function optedIn(el) {
    const scope = el.closest("[data-folio-link], [data-folio-form]");
    return scope && scope.getAttribute(scope.hasAttribute("data-folio-link") ? "data-folio-link" : "data-folio-form") !== "false";
  }

  // Links opt in with data-folio-link on the link or any ancestor (e.g. a
  // <nav>); data-folio-link="false" opts a link back out.
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest && e.target.closest("a[href]");
    if (!a || !optedIn(a) || a.target || a.hasAttribute("download") || !sameOrigin(a.href)) return;
    const url = new URL(a.href);
    if (url.pathname === location.pathname && url.search === location.search && url.hash) return;
    e.preventDefault();
    navigate(a.href);
  });

  // Forms opt in with data-folio-form.
  document.addEventListener("submit", (e) => {
    const form = e.target;
    if (e.defaultPrevented || !(form instanceof HTMLFormElement) || !optedIn(form) || form.target) return;
    const action = actionOf(form, e.submitter);
    if (!sameOrigin(action)) return;
    e.preventDefault();
    submit(form, e.submitter);
  });

  if (window.__FOLIO_DEV__ && window.EventSource) {
    const es = new EventSource("/_folio/live");
    let lost = false;
    es.addEventListener("reload", () => location.reload());
    es.onerror = () => (lost = true);
    es.onopen = () => lost && location.reload();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", emitLoad);
  } else {
    queueMicrotask(emitLoad);
  }

  window.folio = {
    navigate,
    submit,
    cleanup,
    trackEventListener,
    trackObserver,
    get route() {
      return state.route;
    },
    get loaded() {
      return state.loaded;
    },
  };
  // Kept as a global for existing page scripts.
  window.trackEventListener = trackEventListener;
})();
