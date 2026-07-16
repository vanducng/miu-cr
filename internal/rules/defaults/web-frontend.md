---
description: Stack review context for web-frontend components — render safety, effects, and XSS.
globs:
  - "**/*.jsx"
  - "**/*.tsx"
  - "**/*.vue"
  - "**/*.svelte"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Web-frontend review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Render safety and effects

- An effect (React `useEffect`, Vue `watch`) that subscribes, opens a timer, or adds a listener with no teardown leaks on every re-render/unmount — the failure is a growing listener count and stale callbacks firing after unmount.
- A missing or wrong effect dependency array re-runs (or fails to re-run) the effect; the visible bug is a stale closure reading an old prop/state value.
- A list rendered without a stable key (or with the array index as key on a reordering list) corrupts component state across reorders.

Do not report when:
- the handler is attached via JSX/template props (`onClick={...}`, `@click`) — only manual `addEventListener`/subscriptions need teardown.
- a "missing" dependency has stable identity (`setState`, `dispatch`, a ref object) that never changes.
- the index key is on a static list that is never reordered, filtered, or edited.

## Injection and untrusted content

- `dangerouslySetInnerHTML`, `v-html`, or `{@html}` with any value derived from user input is a stored/reflected XSS vector — the concrete failure is script execution in another user's session.
- A URL from user input placed into `href`/`src` without scheme-checking allows `javascript:` URLs.

Do not report when:
- the injected HTML is a build-time constant, repo-owned i18n string, or output of a sanitizer visible in context.
- the `href`/`src` value comes from app config or route constants, not user input.

## State

- Mutating state in place (`arr.push(x)` then setting the same reference) skips the re-render in React; produce a new reference.

Do not report when:
- the mutation targets a fresh local copy (`[...arr]`, `structuredClone`) made in the same scope before the set.
- the mutated object is a ref (`useRef().current`) or non-reactive module state, not render state.
