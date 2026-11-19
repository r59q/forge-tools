# Forge Evidence

Store test results, screenshots, logs and analysis as evidence for claims in
change requests and code reviews. The Go library adds, lists, filters, reads
and validates entries in a local checkout.

The repository strategy keeps ordinary files alongside the code at
`.evidence/<commit>/<entry>/`. Each entry has one or more evidence files and
a version 1 `entry.json` with a title, kind, full subject commit SHA,
optional repository-relative subject path, file sizes and SHA-256 digests.

Default limits are 4 MiB per file, 16 MiB and 32 files per entry, and 64 KiB
for metadata. The limits keep Git history growth in check.

Build and test from the repository root.

```sh
cd forge-evidence/library
CGO_ENABLED=0 go build ./...
go test -race ./...
```

See the [library README](library/README.md) for format rules, scan limits and
API examples. See [future suggestions](FUTURE.md) for S3 storage and service
integration.
