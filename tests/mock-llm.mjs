// Test-only OpenAI-compatible server. Behaviour is driven by the prompt text:
//   "SLOW" in the original -> answers after 3s;  mode "fail" -> HTTP 500.
import http from "node:http";

let fail = false;
const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    if (req.url === "/__mode") {
      fail = JSON.parse(Buffer.concat(chunks).toString()).fail;
      res.end("ok");
      return;
    }
    if (req.url !== "/v1/chat/completions") {
      res.statusCode = 404;
      res.end();
      return;
    }
    const body = JSON.parse(Buffer.concat(chunks).toString());
    const user = body.messages.at(-1).content;
    const original = user.split("--- 原文 ---\n")[1] ?? "";
    const reply = () => {
      if (fail) {
        res.statusCode = 500;
        res.end("boom");
        return;
      }
      // Rename {{name}} to {{user}} so the placeholder warning has both a loss and an addition.
      const content = "优化后：" + original.replace("{{name}}", "{{user}}");
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ choices: [{ message: { content } }] }));
    };
    original.includes("SLOW") ? setTimeout(reply, 3000) : reply();
  });
});
server.listen(18081, "127.0.0.1");
