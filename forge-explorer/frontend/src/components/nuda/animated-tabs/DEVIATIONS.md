# Animated Tabs

Imported literal HTML/CSS/JavaScript: see `nudaui/baseline/animated-tabs` and the
provenance manifest. MIT copyright: Santiago Gómez de la Torre Romero (2026).

`../../tabs.ts` is the maintained TypeScript behavior and dynamic markup adapter.
It replaces the document-wide IIFE with instance-local listeners, keyboard
Left/Right/Home/End navigation, manual activation, Delete-to-close and explicit
ARIA tab/panel relationships. Fixed three-tab sliding geometry is replaced with
an active-tab background so arbitrary filenames and tab counts fit correctly.
The component supports 20 tabs; history supports 100 locations.

Original CSS is used directly. `../../../styles.css` supplies theme tokens,
responsive sizing, contrast, focus indicators and reduced-motion rules within
the instance ShadowRoot. `code.html` and `code.js` remain comparison references;
they are never inserted or executed. Direct edits and larger rewrites are allowed;
record meaningful changes here. Navigation and browser tests cover the adapter.
