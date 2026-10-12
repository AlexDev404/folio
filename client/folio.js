// folio client: in-place page navigation plus lifecycle tracking.
//
// Load it as a classic (non-module) script in <head> so page scripts can use
// the tracking helpers straight away:
//
//   <script src="/_folio/folio.js"></script>
//
// Navigation fetches the next page and swaps the element with id "content"
// (or set data-folio-content on the element to use another id). Timers,
// animation frames, tracked listeners and tracked observers created by the
// old page are cleaned up before the swap. An "appLoad" event is dispatched on
// document after the first load and after every navigation.
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

  function contentElement(root) {
    const marked = document.querySelector("[data-folio-content]");
    const id = (marked && marked.id) || "content";
    return root.getElementById(id);
  }

  function emitLoad() {
    document.dispatchEvent(
      new CustomEvent("appLoad", {
        detail: { timestamp: Date.now(), path: location.pathname },
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

  let generation = 0;

  async function swap(url) {
    const current = ++generation;
    let response;
    try {
      response = await fetch(url, { headers: { "X-Folio-Navigate": "1" } });
    } catch (err) {
      console.error("[folio] navigation failed:", err);
      location.assign(url);
      return;
    }
    if (current !== generation) return; // a newer navigation won

    const html = await response.text();
    if (current !== generation) return;

    const doc = new DOMParser().parseFromString(html, "text/html");
    const next = contentElement(doc);
    const container = contentElement(document);
    if (!next || !container) {
      // Different layout or an error page without #content: load it for real.
      location.assign(url);
      return;
    }

    cleanup();
    document.title = doc.title;
    container.innerHTML = next.innerHTML;
    runScripts(container);
    emitLoad();
  }

  function sameOrigin(url) {
    return new URL(url, location.href).origin === location.origin;
  }

  // Navigate to url without a full page load.
  async function navigate(url, { replace = false } = {}) {
    if (!sameOrigin(url)) {
      location.assign(url);
      return;
    }
    history[replace ? "replaceState" : "pushState"](null, "", url);
    await swap(url);
  }

  window.addEventListener("popstate", () => swap(location.href));

  // Opt-in link interception: <a href="/tools" data-folio-link>.
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest && e.target.closest("a[data-folio-link]");
    if (!a || a.target || !sameOrigin(a.href)) return;
    e.preventDefault();
    navigate(a.href);
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", emitLoad);
  } else {
    queueMicrotask(emitLoad);
  }

  window.folio = { navigate, cleanup, trackEventListener, trackObserver };
  // Kept as a global for existing page scripts.
  window.trackEventListener = trackEventListener;
})();
