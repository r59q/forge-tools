import { element, submit } from "./dom.js";
import type { Transcript, TranscriptChange } from "./transcript.js";
import type { InputAnswer, TranscriptEvent } from "./types.js";

type Answer = (id: string, answer: InputAnswer) => Promise<boolean>;

export class TranscriptView {
  readonly element = element("div", "transcript");
  private readonly collapsible = new Map<number, ActivityView>();
  private readonly forms = new Map<string, HTMLFormElement>();

  constructor(private readonly transcript: Transcript, private readonly answer: Answer) {
    this.element.setAttribute("aria-label", "Transcript");
    this.element.setAttribute("role", "region");
    this.element.tabIndex = 0;
  }

  render(change: TranscriptChange): void {
    const nearBottom = this.element.scrollHeight - this.element.clientHeight - this.element.scrollTop < 64;
    const { block, index, settled } = change;
    if ("entries" in block) {
      const entry = block.entries.at(-1);
      if (entry) this.activity(index, block.title, entry);
    } else {
      this.element.append(this.event(block));
    }
    for (const id of settled) {
      this.forms.get(id)?.remove();
      this.forms.delete(id);
    }
    if (nearBottom) this.element.scrollTop = this.element.scrollHeight;
  }

  private activity(index: number, title: string, entry: TranscriptEvent): void {
    let view = this.collapsible.get(index);
    if (!view) {
      view = new ActivityView();
      this.collapsible.set(index, view);
      this.element.append(view.element);
    }
    view.append(title, entry);
  }

  private event(event: TranscriptEvent): HTMLElement {
    switch (event.kind) {
      case "message": return message(event.role === "user" ? "You" : "Agent", event.text, event.role);
      case "input_request": return this.request(event);
      case "input_answer": return message("Your answer", event.text || (event.allow ? "Allowed" : "Denied"));
      case "result": return message(`Turn ${event.turn} · ${event.status || "ended"}`,
        this.transcript.repeatsMessage(event) ? undefined : event.text, "result");
      case "error": return message("Error", event.text, "error");
      // Sub-session messages delivered by forge-agents are ordinary message events.
      default: return message(event.kind.replaceAll("_", " "), event.text);
    }
  }

  private request(event: TranscriptEvent): HTMLElement {
    const permission = event.requestKind === "permission";
    const label = permission ? `Permission request · ${event.toolName || "Tool"}` : "Question";
    const card = message(label, event.text, "input");
    if (event.input) card.append(element("pre", "", event.input));
    const id = event.requestId;
    if (id) {
      const form = inputForm(permission, answer => this.answer(id, answer));
      this.forms.set(id, form);
      card.append(form);
    }
    return card;
  }
}

function message(label: string, text?: string, style = ""): HTMLElement {
  const card = element("article", `message ${style}`, element("h3", "", label));
  if (text) card.append(element("p", "message-text", text));
  return card;
}

class ActivityView {
  private readonly summary = element("summary", "");
  private readonly entries = element("ol", "");
  readonly element = element("details", "activity", this.summary, this.entries);

  append(title: string, event: TranscriptEvent): void {
    this.summary.textContent = title;
    const entry = element("li", "", element("strong", "", title));
    if (event.input || event.text) entry.append(element("pre", "", event.input || event.text || ""));
    if (event.usage) {
      const { inputTokens = "0", outputTokens = "0", cachedTokens = "0" } = event.usage;
      entry.append(element("p", "", `${inputTokens} input · ${outputTokens} output · ${cachedTokens} cached`));
    }
    if (event.truncated) entry.append(element("small", "", "Truncated by forge-agents"));
    this.entries.append(entry);
  }
}

function inputForm(permission: boolean, answer: (reply: InputAnswer) => Promise<boolean>): HTMLFormElement {
  const text = element("input", "");
  text.setAttribute("aria-label", permission ? "Denial reason" : "Answer");
  text.placeholder = permission ? "Denial reason (optional)" : "Your answer";
  text.required = !permission;
  const send = submit(permission ? "Deny" : "Answer");
  const allow = submit("Allow");
  const controls = element("fieldset", "answer-form", text, send, ...(permission ? [allow] : []));
  const form = element("form", "", controls);
  form.addEventListener("submit", event => {
    event.preventDefault();
    const reply = replyOf(permission, event.submitter === allow, text.value.trim());
    if (!reply) return;
    controls.disabled = true;
    void answer(reply).then(ok => { controls.disabled = ok; });
  });
  return form;
}

function replyOf(permission: boolean, allow: boolean, text: string): InputAnswer | undefined {
  if (!permission) return text ? { text } : undefined;
  return allow ? { allow: true } : { allow: false, text };
}
