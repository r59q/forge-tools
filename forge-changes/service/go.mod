module github.com/r59q/forge-tools/forge-changes/service

go 1.27

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3
	github.com/r59q/forge-tools/forge-connector/library v0.0.0
	github.com/r59q/forge-tools/forge-core/forgeapi v0.0.0
	github.com/r59q/forge-tools/forge-changes/library v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20250929231259-57b25ae835d4
	google.golang.org/grpc v1.75.1
	google.golang.org/protobuf v1.36.10
)

require (
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250929231259-57b25ae835d4 // indirect
)

replace github.com/r59q/forge-tools/forge-core/forgeapi => ../../forge-core/forgeapi

replace github.com/r59q/forge-tools/forge-changes/library => ../library

replace github.com/r59q/forge-tools/forge-connector/library => ../../forge-connector/library
