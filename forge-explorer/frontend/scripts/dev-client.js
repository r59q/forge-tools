// Loaded only by the dev server's page, after app.js. It connects right away
// and reloads the page after each rebuild. The dev server adds the real
// service token to API calls, so the token here is a placeholder.
const form = document.querySelector("#connect");
const token = document.querySelector("#token");

if (form && token) {
  token.value = "dev-server";
  form.requestSubmit();
}

const events = new EventSource("/dev/reload");
events.addEventListener("reload", () => location.reload());
