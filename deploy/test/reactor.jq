# Builds reactor.json from management's connection list. Connections named
# github and forgejo get a webhook source each; $repository on forgejo is
# also polled, when set.

def connection($name):
  first(.connections[]? | select(.name == $name) | .id) // empty;

def webhook($forge; $secret):
  {id: $forge, forge: $forge, connection: ., secretEnv: $secret};

{
  webhooks: [
    (connection("github") | webhook("github"; "FORGE_REACTOR_GITHUB_WEBHOOK_SECRET")),
    (connection("forgejo") | webhook("forgejo"; "FORGE_REACTOR_FORGEJO_WEBHOOK_SECRET"))
  ],
  polls: [
    connection("forgejo")
    | select($repository != "")
    | {id: "forgejo", connection: ., repository: $repository, branches: ["main"]}
  ],
  pollInterval: "1m"
}
