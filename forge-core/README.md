# forge-core

Shared libraries for forge-tools.

Add code here only when several tools need it. Each library is its own
project, e.g. `forge-core/<name>/`, and builds and tests on its own.

forge-core must not depend on any forge-tool.

## Libraries

| Library               | Description                                                 |
| --------------------- | ----------------------------------------------------------- |
| [forgeapi](forgeapi/) | Go: authenticated sessions and HTTP helpers for forge APIs. |
