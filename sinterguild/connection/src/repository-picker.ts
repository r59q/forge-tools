import { listRepositories } from "./repositories.js";
import type { RepositoryIdentity } from "./repositories.js";
import { readRepositorySelection, repositorySelectionKey, RepositorySelection } from "./repository-selection.js";

export type { RepositoryIdentity } from "./repositories.js";

/** Host configuration for the repository picker. */
export interface RepositoryPickerOptions {
  /** Send same-origin requests to management with operator authorization. */
  managementFetch: typeof fetch;
  /** Stable public deployment ID that scopes saved selections within this origin. */
  deploymentId: string;
  /** Receive the confirmed visible identities and advance the host's flow. */
  onComplete: (repositories: readonly RepositoryIdentity[]) => void;
}

const markup = `
  <h1>Select repositories</h1>
  <p role="status">Loading repositories…</p>
  <p role="alert" class="sinterguild-repository-error" hidden></p>
  <form hidden>
    <fieldset aria-label="Visible repositories">
      <div class="sinterguild-repositories"></div>
      <button type="submit" disabled>Confirm selection</button>
    </fieldset>
  </form>
  <template>
    <label class="sinterguild-repository">
      <input type="checkbox">
      <span><strong></strong><small></small></span>
    </label>
  </template>
`;

const listingMessages = {
  empty: "No repositories are visible to the connected forge accounts.",
  unconfigured: "The repository service is unconfigured.",
  failed: "Could not load repositories.",
};

function renderChoices(form: HTMLFormElement, template: HTMLTemplateElement, selection: RepositorySelection): void {
  const rows = form.querySelector(".sinterguild-repositories")!;
  const button = form.querySelector("button")!;
  for (const repository of selection.repositories) {
    const row = template.content.firstElementChild!.cloneNode(true) as HTMLElement;
    const checkbox = row.querySelector("input")!;
    row.querySelector("strong")!.textContent = repository.path;
    row.querySelector("small")!.textContent = `Source ${repository.sourceId}`;
    checkbox.checked = selection.has(repository);
    checkbox.addEventListener("change", () => {
      selection.set(repository, checkbox.checked);
      button.disabled = !selection.hasSelection;
    });
    rows.append(row);
  }
  button.disabled = !selection.hasSelection;
  form.hidden = false;
}

function bindConfirmation(
  form: HTMLFormElement,
  alert: HTMLElement,
  selection: RepositorySelection,
  save: (value: string) => void,
  onComplete: RepositoryPickerOptions["onComplete"],
): void {
  const fields = form.querySelector("fieldset")!;
  form.addEventListener("submit", event => {
    event.preventDefault();
    const repositories = selection.selected();
    if (fields.disabled || repositories.length === 0) {
      return;
    }
    try {
      save(JSON.stringify(repositories));
    } catch {
      alert.textContent = "Could not save your selection in this browser. Try again.";
      alert.hidden = false;
      return;
    }
    alert.hidden = true;
    fields.disabled = true;
    onComplete(repositories);
  });
}

/** Mount the picker and load the repositories visible to this operator. */
export async function mountRepositoryPicker(container: HTMLElement, options: RepositoryPickerOptions): Promise<void> {
  const document = container.ownerDocument;
  const section = document.createElement("section");
  section.className = "sinterguild-repository-picker";
  section.innerHTML = markup;
  container.replaceChildren(section);

  const status = section.querySelector<HTMLElement>('[role="status"]')!;
  const alert = section.querySelector<HTMLElement>('[role="alert"]')!;
  const listing = await listRepositories(options.managementFetch);
  if (listing.state !== "ready") {
    status.textContent = listingMessages[listing.state];
    return;
  }

  const storageKey = repositorySelectionKey(options.deploymentId);
  const storage = () => document.defaultView!.localStorage;
  const selection = new RepositorySelection(listing.repositories, () => storage().getItem(storageKey));
  const form = section.querySelector("form")!;
  bindConfirmation(form, alert, selection, value => storage().setItem(storageKey, value), options.onComplete);
  renderChoices(form, section.querySelector("template")!, selection);
  status.hidden = true;
}

/** Read the completed repository preference in the host's browser profile. */
export function loadRepositorySelection(deploymentId: string): readonly RepositoryIdentity[] {
  return readRepositorySelection(() => localStorage.getItem(repositorySelectionKey(deploymentId)));
}
