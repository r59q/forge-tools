import { element, field, submit } from "../src/dom.js";
import { ChatRequestError, httpChat, mountChat } from "../src/index.js";

const root = document.querySelector<HTMLElement>("#app");
if (root) signIn(root);

function signIn(root: HTMLElement): void {
  const token = element("input", "");
  token.type = "password";
  token.required = true;
  token.autocomplete = "off";
  const error = element("p", "chat-error");
  error.setAttribute("role", "alert");
  const send = submit("Open chats");
  const form = element("form", "sign-in", element("h1", "", "forge-chat"),
    field("Chat service token", token), error, send);
  form.addEventListener("submit", event => {
    event.preventDefault();
    const value = token.value.trim();
    if (!value) return;
    send.disabled = true;
    error.textContent = "";
    const provider = httpChat({ token: () => value });
    void provider.metadata(AbortSignal.timeout(10_000)).then(() => {
      token.value = "";
      const host = element("div", "chat-host");
      const unmount = mountChat(host, provider);
      const signOut = element("button", "", "Sign out");
      signOut.addEventListener("click", () => {
        unmount();
        signIn(root);
      });
      root.replaceChildren(element("div", "app-actions", signOut), host);
    }).catch((cause: unknown) => {
      send.disabled = false;
      error.textContent = cause instanceof ChatRequestError && cause.status === 401
        ? "The service did not accept this token." : "Could not reach the chat service.";
    });
  });
  root.replaceChildren(form);
  token.focus();
}
