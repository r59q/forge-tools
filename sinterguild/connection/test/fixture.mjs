import { readFile } from "node:fs/promises";
import { createServer } from "node:http";

export async function serveFixture(t, hostPage, modules) {
  const assets = new Map([
    ["/src/styles.css", "text/css"],
    ...modules.map(name => [`/dist/${name}.js`, "text/javascript"]),
  ]);
  const server = createServer(async (request, response) => {
    const { pathname } = new URL(request.url, "http://127.0.0.1");
    if (pathname === "/") {
      response.writeHead(200, { "Content-Type": "text/html" }).end(hostPage);
      return;
    }
    const type = assets.get(pathname);
    if (!type) {
      response.writeHead(404).end();
      return;
    }
    const content = await readFile(new URL(`..${pathname}`, import.meta.url));
    response.writeHead(200, { "Content-Type": type }).end(content);
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  return `http://127.0.0.1:${server.address().port}`;
}
