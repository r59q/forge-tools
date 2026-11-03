# servicekit

Go library with the startup code every forge-tools service shares:

- `Parse`: shared flags and `PREFIX_*` environment variables.
- `BearerToken`: a service or connector token from a file or a variable.
- `Authorize` and `Call`: bearer-token checks and unary RPCs that return
  only status codes.
- `NewMux`, `Secure` and `RESTError`: a hardened grpc-gateway REST handler.
- `Server`, `Shutdown` and `Main`: listen, serve and shut down gracefully.

Module: `github.com/r59q/forge-tools/forge-core/servicekit`

## Use

```go
func main() {
	servicekit.Main(func(ctx context.Context) error {
		s, err := servicekit.Parse(os.Args[1:], os.Getenv, os.Stdout, servicekit.Options{
			Name:       "forge-example-service",
			Prefix:     "FORGE_EXAMPLE",
			GRPCListen: "127.0.0.1:9090",
			HTTPListen: "127.0.0.1:8080",
			Timeout:    "30s",
		}, nil)
		if err != nil {
			return err
		}

		token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
		if err != nil {
			return err
		}

		// Build the API with token, then serve it.
		return servicekit.Server{ /* addresses, Register, Handler */ }.Run(ctx)
	})
}
```

## Build and test

```sh
go vet ./...
go test ./...
```

## License

[MIT](../../LICENSE)
