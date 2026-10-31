# forgeapi

Go library for talking to forge APIs: GitHub, GitLab and Forgejo. It uses
only the standard library.

```text
forgeauth/                  credentials, sessions, providers, registry
  github/ gitlab/ forgejo/  one provider per forge
forgehttp/                  errors, Link pagination, rate limits
```

Module: `github.com/r59q/forge-tools/forge-core/forgeapi`

## forgeauth

Opens an authenticated `*http.Client` for a forge.

```go
var reg forgeauth.Registry
reg.Register(github.Provider{})
reg.Register(gitlab.Provider{})
reg.Register(forgejo.Provider{})

s, err := reg.Open(ctx, forgeauth.Connection{
	Kind:    forgeauth.GitLab,
	BaseURL: "https://gitlab.com/api/v4",
}, forgeauth.EnvToken("GITLAB_TOKEN"))
if err != nil {
	return err
}
me, err := reg.Whoami(ctx, s) // GET /user
```

- Credentials: `StaticToken`, `EnvToken` and `FileToken`. Tokens are read
  for each request, so a rotated file or variable takes effect at once.
- Auth headers: GitHub `Authorization: Bearer`, GitLab `PRIVATE-TOKEN`,
  Forgejo `Authorization: token`. Each provider package also has
  `TokenFromHeader` to read a token from an incoming request.
- The session's client sends the token only to the session's own scheme
  and host, also across redirects and for absolute URLs.
- Tokens are not printed by `fmt`, `log/slog` or `encoding/json`, and do not
  appear in errors.
- Nothing is registered by default. Register the providers you need.

## forgehttp

- `CheckResponse` turns a 4xx or 5xx response into a `*forgehttp.Error`.
  Use `errors.Is` with `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`,
  `ErrConflict` or `ErrRateLimited`.
- `ParseLinks` and `NextPage` read `Link` headers. `Links.String` writes
  them back.
- `ParseRateLimit` reads `Retry-After`, `X-RateLimit-*` and `RateLimit-*`.

`forgehttp` does not import `forgeauth`.

## Develop

From this directory:

```sh
gofmt -l .
go vet ./...
go test ./...
```
