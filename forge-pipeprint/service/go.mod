module github.com/r59q/forge-tools/forge-pipeprint/service

go 1.27

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3
	github.com/r59q/forge-tools/forge-core/forgeapi v0.0.0
	github.com/r59q/forge-tools/forge-core/servicekit v0.0.0
	github.com/r59q/forge-tools/forge-pipeprint/library v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20250929231259-57b25ae835d4
	google.golang.org/grpc v1.75.1
	google.golang.org/protobuf v1.36.10
)

require (
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250929231259-57b25ae835d4 // indirect
)

replace github.com/r59q/forge-tools/forge-core/forgeapi => ../../forge-core/forgeapi

replace github.com/r59q/forge-tools/forge-core/servicekit => ../../forge-core/servicekit

replace github.com/r59q/forge-tools/forge-pipeprint/library => ../library
