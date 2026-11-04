# Component catalogue

These framework-agnostic TypeScript parts are internal implementation boundaries,
not new package exports. Names below match functions or classes. Components use
`dom.ts` to render text nodes; repository text never becomes HTML.

| Names | Implementation | Responsibility and states |
| --- | --- | --- |
| Button | [controls.ts](src/components/controls.ts) | Compact NudaUI action; native button/submit, disabled and focus states. |
| Input, Select, Field | [controls.ts](src/components/controls.ts) | Native text/search/password/number inputs, options and associated labels; required, bounded, disabled and focus states. |
| Link, Disclosure | [controls.ts](src/components/controls.ts) | Native anchors and details/summary; keyboard activation, collapsed/expanded. |
| Text | [messages.ts](src/components/messages.ts) | Plain, muted, caption and code treatments. |
| StatusMessage, EmptyState, ResultSummary | [messages.ts](src/components/messages.ts) | Polite live status and empty content; summary text includes skipped, truncated and canceled results. Callers supply loading/error messages. |
| Separator, ScrollArea | [layout.ts](src/components/layout.ts) | Decorative punctuation; overflow on the original semantic element. |
| Toolbar | [layout.ts](src/components/layout.ts) | Related controls in a wrapping row; normal Tab navigation. |
| Panel, PanelHeader, PanelBody | [layout.ts](src/components/layout.ts) | Semantic panel with controls/status above content. |
| Tabs, TabList, Tab, TabPanel | [tabs.ts](src/components/tabs.ts) | Scrollable tabs, selected styling, roving focus and explicit tab/panel relationships. |
| Breadcrumbs, BreadcrumbItem, BreadcrumbSeparator | [breadcrumbs.ts](src/components/breadcrumbs.ts) | Parent actions, current-file text and decorative separators. |
| ExplorerShell, SourceHeader, ExplorerSidebar, CapabilitySummary | [shell.ts](src/components/shell.ts) | Responsive source layout; live/versioned identity, tree/search and available/unsupported capabilities. |
| ExplorerToolbar | [toolbar.ts](src/components/toolbar.ts) | Back, forward, reload and close; enabled by navigation state. |
| FileTree | [tree.ts](src/components/tree.ts) | Lazy, scrollable directory listing; loading, empty, error, partial and canceled results; Up/Down focus. |
| FileTreeItem | [tree-item.ts](src/components/tree-item.ts) | File action, native directory disclosure or disabled unsupported entry. Expanded directories load once. |
| SearchPanel | [search.ts](src/components/search.ts) | Renders the DOM-free search model; idle, loading, error, empty, partial and canceled states. |
| SearchBar, SearchOptions | [search-parts.ts](src/components/search-parts.ts) | Validated query submission, cancellation, text/file mode and optional path glob. |
| SearchResults, SearchResultItem | [search-parts.ts](src/components/search-parts.ts) | Scrollable result list, Up/Down focus, source/version-bound actions and text previews. |
| FileWorkspace | [file-view.ts](src/components/file-view.ts) | Tabs, path, status and current file; retains existing content during loading and failed/stale navigation. |
| FileTabs, FileBreadcrumbs | [tabs.ts](src/components/tabs.ts), [breadcrumbs.ts](src/components/breadcrumbs.ts) | File-specific activation, Delete-to-close and directory navigation. |
| FileMetadata, SelectionSummary | [file-metadata.ts](src/components/file-metadata.ts) | Live path/version, loading/error status and selected line range. |
| CodeViewer | [code.ts](src/components/code.ts) | At most 200 lines; pagination, selection, Shift extension and roving line focus. |
| CodeLine, LineNumber | [code-parts.ts](src/components/code-parts.ts) | Safe highlighting and specialised pressed/selected line controls. |
| CodeNavigation, GoToLine | [code-parts.ts](src/components/code-parts.ts) | Previous/next, visible range and bounded number input; Enter moves focus. |
| AppHeader, ConnectionForm, SourceSelector, ConnectionStatus, AttributionFooter | [app/components.ts](app/components.ts) | Standalone introduction, native token form, source choice, live connection states and upstream credit. |

Navigation remains in `src/model/navigation.ts`. Search requests, validation and
cancellation live in `src/model/search.ts`; views own DOM and focus. The public
embedding API and source/version contract are unchanged. Disposal cancels only
that instance. Fixed tab IDs are scoped to each instance's ShadowRoot.

`src/components/primitives.css` supplies shared compact controls, text treatments,
focus and reduced motion in both the app and ShadowRoot. `src/styles.css` owns
explorer layout, tree/search rows, tabs and code. `app/app.css` owns page layout.
Per-instance `--explorer-*` theme variables remain on the host. Tree rows retain discovery states through `FileTreeItem`; tabs and line numbers
use specialised controls for their ARIA and roving-focus semantics.

NudaUI baselines and provenance are unchanged. Maintained adaptation notes:
[Shine Sweep](src/components/nuda/shine-sweep/DEVIATIONS.md) and
[Animated Tabs](src/components/nuda/animated-tabs/DEVIATIONS.md).

**Planned additions:** Icon, IconButton and Tooltip. No implementation or new
visual design is introduced for these parts in this extraction.
