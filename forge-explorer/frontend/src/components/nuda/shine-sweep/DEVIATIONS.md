# Shine Sweep

Imported HTML/CSS: see `nudaui/baseline/shine-sweep` and the provenance manifest.
MIT copyright: Santiago Gómez de la Torre Romero (2026).

`../../controls.ts` (`Button`) builds the imported button/span markup with textContent and an
explicit button type. Labels may contain repository data. Original CSS is used
directly; `../../primitives.css` supplies explorer tokens, compact spacing, focus,
disabled states and reduced motion. The standalone app and each ShadowRoot
use the same adapter and styles. Specialised file-tree items keep the imported
button appearance and disabled unsupported-entry state. No upstream script exists.
Direct edits are allowed; record meaningful deviations here. Browser tests cover
keyboard activation, safe text rendering and instance isolation.
