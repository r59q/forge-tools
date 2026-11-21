interface SignInOptions {
  label: string;
  action: string;
  open: (token: string) => Promise<boolean>;
}

/** Prompt for a token and leave the form in place when authentication fails. */
export function mountSignIn(root: HTMLElement, options: SignInOptions): void {
  const template = document.querySelector<HTMLTemplateElement>("#sign-in")!;
  const form = template.content.querySelector("form")!.cloneNode(true) as HTMLFormElement;
  const token = form.querySelector("input")!;
  const button = form.querySelector("button")!;
  const error = form.querySelector('[role="alert"]')!;
  form.querySelector("label span")!.textContent = options.label;
  button.textContent = options.action;
  form.addEventListener("submit", async event => {
    event.preventDefault();
    const value = token.value.trim();
    if (button.disabled || !value) return;
    button.disabled = true;
    token.value = "";
    error.textContent = "";
    try {
      if (await options.open(value)) return;
      error.textContent = "The service did not accept this token.";
    } catch {
      error.textContent = "Could not reach the service. Try again.";
    }
    button.disabled = false;
    token.focus();
  });
  root.replaceChildren(form);
  token.focus();
}
