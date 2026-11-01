module github.com/r59q/forge-tools/forge-tools-management/library

go 1.27

require github.com/r59q/forge-tools/forge-repositories/library v0.0.0

require github.com/r59q/forge-tools/forge-core/forgeapi v0.0.0 // indirect

replace github.com/r59q/forge-tools/forge-repositories/library => ../../forge-repositories/library

replace github.com/r59q/forge-tools/forge-core/forgeapi => ../../forge-core/forgeapi
