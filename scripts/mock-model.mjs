// Deterministic stand-in for an OpenAI-compatible endpoint, for verifying the
// evaluation pipeline with no real key and no billing.
//
//   bun scripts/mock-model.mjs            # listens on 127.0.0.1:19100
//   EVAL_BASE_URL=http://127.0.0.1:19100/v1 EVAL_API_KEY=mock EVAL_MODEL=mock \
//     bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out /tmp/eval-out
//
// It answers the *task* (it never echoes prompt-rewriting meta text), and it is
// deliberately imperfect: it always answers the same way regardless of the
// prompt it was given, so the checks come out flat and the corpus clearly
// demonstrates that a mock run proves the pipeline, not the prompt quality.

import http from "node:http";

const PORT = Number(process.env.MOCK_PORT ?? 19100);

// One canned answer per task family, chosen from the task input itself.
function answer(input) {
  if (input.includes("func Sum")) {
    return "1. 严重：循环条件 i <= len(xs) 会越界访问。触发条件：xs 非空且循环走完。修复：改为 i < len(xs)。";
  }
  if (input.includes('"name"') || input.includes("employee_id") || input.includes("工号")) {
    return JSON.stringify({ name: "张伟", department: "销售部", employee_id: "1024", hire_date: "2023-05-06" });
  }
  if (input.includes("128 元")) {
    return "您好，您反馈的问题我们已收到，会在 3 个工作日内处理完成并回复您，退款金额 128 元原路退回。给您带来不便，深表歉意。";
  }
  if (input.includes("纪要") || input.includes("小李")) {
    return "参会人发言：\n- 小李：后端接口下周一二前给测试版。\n- 小王：前端等接口，先做布局。\n决议：4 月 18 日前完成联调。\n待办：负责人会上未定。\n待定：日志保留多久，会上未定。";
  }
  return `任务结果（模拟模型）：针对输入「${input.slice(0, 40)}」的结果。`;
}

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    if (req.url !== "/v1/chat/completions") {
      res.statusCode = 404;
      res.end();
      return;
    }
    let body;
    try {
      body = JSON.parse(Buffer.concat(chunks).toString());
    } catch {
      res.statusCode = 400;
      res.end();
      return;
    }
    const user = body.messages?.at(-1)?.content ?? "";
    if (req.headers.authorization !== "Bearer mock") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ error: { message: "bad key" } }));
      return;
    }
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ choices: [{ message: { content: answer(user) } }] }));
  });
});

server.listen(PORT, "127.0.0.1", () => console.log(`mock model on http://127.0.0.1:${PORT}/v1`));
