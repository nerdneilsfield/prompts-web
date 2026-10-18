# 演示数据与评测

本页包含两项独立操作：向自己的库导入演示条目；用评测脚本比较原始 Prompt 与优化后 Prompt 的任务表现。两项都由你手动执行，服务启动时不写入任何数据。

## 导入演示数据

`fixtures/demo.json` 含 6 条 Prompt 和 3 个方案，每条 Prompt 都带 `演示` 标签。服务运行后执行：

```bash
bun scripts/import-demo.mjs --url http://127.0.0.1:8080 --password "$APP_PASSWORD"
```

脚本通过登录接口和 CRUD 接口写入，数据与手工添加的条目存放方式相同。标题相同的 Prompt 与名称相同的方案会跳过并提示「已存在」，重复执行不产生副本。
脚本也读取环境变量 `APP_URL` 和 `APP_PASSWORD`。它只创建 Prompt 与方案，不创建优化运行。

导入后，在库里点 `演示` 标签即可筛出这些条目，接口等价写法是 `GET /api/prompts?tag=演示`。

## 评测脚本

`scripts/eval.mjs` 比较同一个模型在两种 Prompt 下完成同一任务的结果。模型执行的是任务本身，例如写纪要、审代码、改邮件、抽取 JSON。
脚本只调用 OpenAI 兼容的 `/chat/completions`，不登录站点，不调用 `/api/optimizations`。
优化结果可以先在工作台里生成，再填入样本的 `optimized` 字段。

```bash
export EVAL_BASE_URL=https://api.example.com/v1
export EVAL_API_KEY=你的密钥
export EVAL_MODEL=模型名

bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out eval-out
```

三个变量缺任意一个，脚本报配置错误并退出。

### 样本格式

样本是 JSONL，每行一个用例：

```json
{"id":"json-extract","input":"张伟，销售部，工号 1024，入职 2023-05-06。",
 "original":"把下面这段信息整理成 JSON：\n{{输入}}",
 "optimized":"把下面文本中的字段抽取为 JSON 对象……\n{{输入}}",
 "checks":{"kind":"json_object","required_keys":["name","department","employee_id","hire_date"],"max_chars":4000},
 "rubric":"键名是否完全一致；日期格式是否为 YYYY-MM-DD；是否有多余文字。"}
```

- `{{输入}}` 是任务输入的代入位置。模板里没有它时，输入以 `--- 待处理内容 ---` 为分隔符追加在末尾。
- 每个输入在两种 Prompt 下各运行 3 次，共 6 次模型调用。两组使用相同的 system 消息、任务输入和采样参数（`temperature=0.2`、`top_p=1`、`max_tokens=2048`），只有待测 Prompt 文本不同。
- `checks` 支持四项：合法 JSON、必需键、必须出现的子串、最大 Unicode 字符数。没有 `checks` 的用例交给人工 A/B 盲评。
- 结果不按长度打分，也不由模型打分。

### 输出文件

输出写入 `--out` 目录（默认 `eval-out/`，已被 git 忽略）：

| 文件 | 内容 |
|---|---|
| `responses.jsonl` | 每次调用一行：样本、分组、序号、模型、参数、状态、错误、耗时、完整 system/prompt/输出 |
| `blind-items.jsonl` | 随机 A/B 盲评材料：`item_id`、任务输入、a/b 两段输出、rubric |
| `blind-map.json` | 每个 item 的 `optimized_side`，以及来源样本和运行序号 |
| `blind-annotations.template.json` | 待填写的投票模板 |
| `summary.json` | 检查通过数、失败计数、逐样本计数、退化样本、盲评统计 |

### 盲评与汇总

```bash
cp eval-out/blind-annotations.template.json eval-out/blind-annotations.json
# 逐项填 "a"、"b" 或 "tie"，null 表示不评
bun scripts/eval.mjs --summarize eval-out --annotations eval-out/blind-annotations.json
```

`--summarize` 只读取 `responses.jsonl` 与 `blind-map.json`，不调用模型，可以反复执行。
输出包含优化胜、原始胜、平票和未评分的条数；优化组通过检查的轮次少于原始组的样本，列在 `degenerate_samples` 里。

### 用模拟模型检查流程

`scripts/mock-model.mjs` 是固定答案的模拟模型，不需要密钥，不产生费用：

```bash
bun scripts/mock-model.mjs &
EVAL_BASE_URL=http://127.0.0.1:19100/v1 EVAL_API_KEY=mock EVAL_MODEL=mock \
  bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out /tmp/eval-out
```

模拟模型对两种 Prompt 给出相同答案，两组检查结果相同。这个运行用来确认脚本、输出文件和汇总流程可用，结论数字需要真实模型的运行。
`fixtures/eval-samples.jsonl` 覆盖写纪要、审代码、改邮件、抽取 JSON 四类任务，每个输入重复 3 次，用来观察单次运行的波动。
