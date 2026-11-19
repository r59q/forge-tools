import { element, field, option, submit } from "./dom.js";
import type { NewAgent } from "./types.js";

function select(values: [string, string][]): HTMLSelectElement {
  return element("select", "", ...values.map(([value, label]) => option(value, label)));
}

function input(placeholder: string): HTMLInputElement {
  const control = element("input", "");
  control.placeholder = placeholder;
  return control;
}

function efforts(client: string): HTMLOptionElement[] {
  const common = ["low", "medium", "high", "xhigh"];
  const values = client === "claude" ? [...common, "max"] : ["none", "minimal", ...common];
  const choices = client === "gemini" ? [] : values.map(value => option(value, value));
  return [option("", "Client default"), ...choices];
}

export function createForm(create: (agent: NewAgent) => Promise<boolean>): HTMLDetailsElement {
  const client = select([["claude", "Claude Code"], ["codex", "Codex"], ["gemini", "Gemini"]]);
  const model = input("Client default");
  const effort = element("select", "", ...efforts(client.value));
  const permission = element("input", "");
  permission.type = "checkbox";
  const permissionLabel = element("label", "check", permission, "Ask for tool permission");
  client.addEventListener("change", () => {
    permissionLabel.hidden = client.value !== "claude";
    effort.replaceChildren(...efforts(client.value));
    effort.disabled = client.value === "gemini";
  });

  const checkout = checkoutFields();
  const send = submit("Create chat");
  const form = element("form", "create-form",
    field("Client", client), field("Model", model), field("Effort", effort), permissionLabel, checkout.element, send);
  const details = element("details", "create-chat", element("summary", "", "New chat"), form);
  form.addEventListener("submit", event => {
    event.preventDefault();
    const spec: NewAgent = {
      client: client.value, model: model.value.trim(), effort: effort.value.trim(), workspace: checkout.workspace(),
    };
    if (client.value === "claude" && permission.checked) spec.policy = { mode: "read_only", ask: true };
    send.disabled = true;
    void create(spec).then(ok => {
      send.disabled = false;
      if (ok) details.open = false;
    });
  });
  return details;
}

function checkoutFields() {
  const kind = select([["scratch", "Scratch directory"], ["forge", "New worktree · forge"], ["local", "New worktree · local"]]);
  const source = input("Connection ID");
  const path = input("owner/repository");
  const local = input("/absolute/repository/path");
  const forge = element("div", "workspace-paths", field("Source", source), field("Repository", path));
  const localField = field("Repository path", local);
  const update = () => {
    forge.hidden = kind.value !== "forge";
    localField.hidden = kind.value !== "local";
    source.required = path.required = !forge.hidden;
    local.required = !localField.hidden;
  };
  kind.addEventListener("change", update);
  update();
  return {
    element: element("div", "workspace-paths", field("Workspace", kind), forge, localField),
    workspace(): NewAgent["workspace"] {
      switch (kind.value) {
        case "forge": return { kind: "checkout", repository: { source: source.value.trim(), path: path.value.trim() } };
        case "local": return { kind: "checkout", repository: { localPath: local.value.trim() } };
        default: return undefined;
      }
    },
  };
}
