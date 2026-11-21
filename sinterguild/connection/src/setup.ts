import { addConnection } from "./management.js";
import type { AcceptedConnection } from "./management.js";

export type { AcceptedConnection } from "./management.js";

/** Host-supplied management transport and successful setup callback. */
export interface ConnectionSetupOptions {
  /** Send same-origin requests to management with operator authorization. */
  managementFetch: typeof fetch;
  /** Receive the accepted public ID and advance the host's flow. */
  onConnected: (connection: AcceptedConnection) => void;
}

const formMarkup = `
  <h1>Connect a forge</h1>
  <p>Add a forge account to access its repositories.</p>
  <p role="alert" class="sinterguild-connection-error" hidden></p>
  <form method="post" autocomplete="off">
    <fieldset aria-label="Forge connection details">
      <label>Forge
        <select name="forge">
          <option value="github">GitHub</option>
          <option value="gitlab">GitLab</option>
          <option value="forgejo">Forgejo</option>
        </select>
      </label>
      <label>Host (optional)
        <input name="host" inputmode="url" autocapitalize="none" spellcheck="false" placeholder="gitlab.example.com">
      </label>
      <label>Connection name (optional)
        <input name="name">
      </label>
      <label>Access token
        <input name="credential" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" required>
      </label>
      <button type="submit">Connect forge</button>
    </fieldset>
  </form>
`;

/** Mount a fresh connection step inside the host's container. */
export function mountConnectionSetup(container: HTMLElement, options: ConnectionSetupOptions): void {
  const section = container.ownerDocument.createElement("section");
  section.className = "sinterguild-connection";
  section.innerHTML = formMarkup;
  const form = section.querySelector("form")!;
  const fields = form.querySelector("fieldset")!;
  const token = form.querySelector<HTMLInputElement>('input[name="credential"]')!;
  const alert = section.querySelector<HTMLElement>('[role="alert"]')!;
  const button = form.querySelector("button")!;

  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (fields.disabled) {
      return;
    }

    const values = new FormData(form);
    token.value = "";
    alert.hidden = true;
    fields.disabled = true;
    button.textContent = "Connecting…";

    const resultPromise = addConnection(options.managementFetch, {
      forge: String(values.get("forge")),
      host: String(values.get("host")),
      name: String(values.get("name")),
      credential: String(values.get("credential")),
    });
    values.delete("credential");
    const result = await resultPromise;
    if ("connection" in result) {
      button.textContent = "Forge connected";
      options.onConnected(result.connection);
      return;
    }

    alert.textContent = result.error;
    alert.hidden = false;
    fields.disabled = false;
    button.textContent = "Connect forge";
    token.focus();
  });

  container.replaceChildren(section);
}
