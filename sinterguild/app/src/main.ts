import { ChatRequestError, httpChat, mountChat } from "../../../forge-chat/frontend/src/index.js";
import { loadRepositorySelection, mountRepositoryPicker } from "../../connection/src/repository-picker.js";
import type { RepositoryIdentity } from "../../connection/src/repository-picker.js";
import { mountConnectionSetup } from "../../connection/src/setup.js";
import { mountSignIn } from "./sign-in.js";

const root = document.querySelector<HTMLElement>("#app")!;
const deploymentId = "sinterguild";
const selection = loadRepositorySelection(deploymentId);
if (selection.length) openChat(selection);
else openSetup();

function openSetup(): void {
  mountSignIn(root, {
    label: "Management operator token",
    action: "Begin setup",
    async open(token) {
      const managementFetch = managementTransport(token);
      const reply = await managementFetch("/v1/connections", { signal: AbortSignal.timeout(10_000) });
      await reply.body?.cancel();
      if (accessDenied(reply.status)) return false;
      if (!reply.ok) throw new Error("Management unavailable");

      mountConnectionSetup(root, {
        managementFetch,
        onConnected: () => { void mountRepositoryPicker(root, { managementFetch, deploymentId, onComplete: openChat }); },
      });
      return true;
    },
  });
}

function managementTransport(token: string): typeof fetch {
  return (path, options) => {
    const headers = new Headers(options?.headers);
    headers.set("Authorization", `Bearer ${token}`);
    return fetch("/api/management" + path, { ...options, headers, credentials: "omit", redirect: "error" });
  };
}

function openChat(repositories: readonly RepositoryIdentity[]): void {
  mountSignIn(root, {
    label: "Chat service token",
    action: "Open chats",
    async open(token) {
      const provider = httpChat({ baseURL: "/api/chat", token: () => token });
      try {
        await provider.metadata(AbortSignal.timeout(10_000));
      } catch (error) {
        if (error instanceof ChatRequestError && accessDenied(error.status)) return false;
        throw error;
      }

      const template = document.querySelector<HTMLTemplateElement>("#chat-layout")!;
      const layout = template.content.cloneNode(true) as DocumentFragment;
      const dispose = mountChat(layout.querySelector<HTMLElement>(".chat-host")!, provider, { repositories });
      layout.querySelector("button")!.addEventListener("click", () => {
        dispose();
        openChat(repositories);
      });
      root.replaceChildren(layout);
      return true;
    },
  });
}

function accessDenied(status: number): boolean {
  return status === 401 || status === 403;
}
