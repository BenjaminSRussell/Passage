import http from "node:http";

export function createServer() {
  return http.createServer((req, res) => {
    const url = req.url || "/";
    if (url === "/healthz") {
      const body = JSON.stringify({ status: "ok", service: "platform-bff" });
      res.writeHead(200, { "content-type": "application/json" });
      res.end(body);
      return;
    }
    res.writeHead(404, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: "not found" }));
  });
}

const isMain = process.argv[1] && process.argv[1].endsWith("server.js");
if (isMain) {
  const port = Number(process.env.PORT || 8081);
  const server = createServer();
  const bind = process.env.BIND_ADDR || "127.0.0.1";
  server.listen(port, bind, () => {
    console.log(`platform-bff listening on ${bind}:${port}`);
  });
}
