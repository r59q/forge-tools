module github.com/r59q/forge-tools/forge-search/library

go 1.27

require (
	github.com/r59q/forge-tools/forge-issues/library v0.0.0
	github.com/r59q/forge-tools/forge-symbols/library v0.0.0
	golang.org/x/sync v0.16.0
	modernc.org/sqlite v1.39.1
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/r59q/forge-tools/forge-core/forgeapi v0.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/sys v0.44.0 // indirect
	modernc.org/libc v1.66.10 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/r59q/forge-tools/forge-issues/library => ../../forge-issues/library

replace github.com/r59q/forge-tools/forge-core/forgeapi => ../../forge-core/forgeapi

replace github.com/r59q/forge-tools/forge-symbols/library => ../../forge-symbols/library
