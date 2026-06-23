---
description: Stack review context for web-frontend components — render safety, effects, and XSS.
globs:
  - "**/*.jsx"
  - "**/*.tsx"
  - "**/*.vue"
  - "**/*.svelte"
alwaysApply: false
---
# Web-frontend review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Render safety and effects

- An effect (React `useEffect`, Vue `watch`) that subscribes, opens a timer, or adds a listener with no teardown leaks on every re-render/unmount — the failure is a growing listener count and stale callbacks firing after unmount.
- A missing or wrong effect dependency array re-runs (or fails to re-run) the effect; the visible bug is a stale closure reading an old prop/state value.
- A list rendered without a stable key (or with the array index as key on a reordering list) corrupts component state across reorders.

## Injection and untrusted content

- `dangerouslySetInnerHTML`, `v-html`, or `{@html}` with any value derived from user input is a stored/reflected XSS vector — the concrete failure is script execution in another user's session.
- A URL from user input placed into `href`/`src` without scheme-checking allows `javascript:` URLs.

## State

- Mutating state in place (`arr.push(x)` then setting the same reference) skips the re-render in React; produce a new reference.
