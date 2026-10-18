// Small evaluation harness for the prompt library.
//
// It measures one thing: given the same task input and the same model, does a
// prompt produce a better task result than the prompt it was derived from?
// The model executes the task (a summary, a review, an email, an extraction),
// it does not rewrite the prompt.
//
//   export EVAL_BASE_URL=https://api.example.com/v1
//   export EVAL_API_KEY=sk-...        # read from the environment only
//   export EVAL_MODEL=some-model
//   bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out eval-out
//
// Each arm is called 3 times per input with identical parameters, so 6 calls
// per input. Samples are JSONL: {"id","input","original","optimized","checks"?,"rubric"?}.
// Both arms get the identical system message, the same task input and the same
// sampling parameters; only the prompt under test differs.
//
// What this does not do:
//   - no site login, no scheme, no /api/optimizations, no library coupling;
//     the site's optimizer may well be a different model, and optimizing the
//     prompt is not what is being measured here
//   - no length scoring and no model-as-judge: only the sample's own executable
//     checks (legal JSON, required keys, required substrings, max codepoints)
//   - annotations are counted offline from a file; scoring never re-calls a
//     model, so a second summary pass costs nothing
//
// Demo samples prove the pipeline runs; they are not evidence of quality.

import { appendFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { parseArgs } from "./lib/client.mjs";

const ARMS = ["original", "optimized"];
const DEFAULT_RUNS = 3;
const DEFAULT_PARAMS = { temperature: 0.2, top_p: 1, max_tokens: 2048 };

// Turn the sample into the single user message actually sent.
//   "…说明…\n\n{{输入}}"  -> instruction, then the task input on its own
//   "…说明…"              -> the input is appended as the content to operate on
export function buildUserMessage(template, input) {
  if (template.includes("{{输入}}")) return template.split("{{输入}}").join(input);
  return `${template}\n\n--- 待处理内容 ---\n${input}`;
}

function makeRandom(seed) {
  if (seed === undefined || seed === null || seed === "") return Math.random;
  let state = Number(seed) >>> 0;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 4294967296;
  };
}

export const codepoints = (s) => [...s].length;

// One outcome per check, so a failure names the mechanism that failed.
export function runChecks(text, checks) {
  if (!checks) return { scored: false, passed: null, outcomes: [] };
  const outcomes = [];
  const add = (name, passed, detail) => outcomes.push({ name, passed, detail });

  for (const needle of checks.must_include_all ?? []) {
    const found = text.includes(needle);
    add(`must_include_all:${needle}`, found, found ? "found" : "missing");
  }
  const anyOf = checks.must_include_any ?? [];
  if (anyOf.length > 0) {
    const hit = anyOf.find((needle) => text.includes(needle));
    add(`must_include_any:${anyOf.join("|")}`, hit !== undefined, hit === undefined ? "none present" : `found ${hit}`);
  }
  if (typeof checks.max_chars === "number") {
    const n = codepoints(text);
    add(`max_chars:${checks.max_chars}`, n <= checks.max_chars, `${n} codepoints`);
  }
  if (checks.kind === "json_object") {
    let parsed = null;
    let error = null;
    try {
      parsed = JSON.parse(text.trim());
    } catch (e) {
      error = e.message;
    }
    const isObject = parsed !== null && typeof parsed === "object" && !Array.isArray(parsed);
    add("json_object", isObject, error ?? (isObject ? "parsed" : "parsed but not an object"));
    const required = checks.required_keys ?? [];
    if (isObject && required.length > 0) {
      const missing = required.filter((k) => !(k in parsed));
      add(`required_keys:${required.join(",")}`, missing.length === 0, missing.length === 0 ? "all present" : `missing ${missing.join(",")}`);
    }
  }
  return { scored: outcomes.length > 0, passed: outcomes.length > 0 ? outcomes.every((o) => o.passed) : null, outcomes };
}

function configFromEnv() {
  const baseUrl = process.env.EVAL_BASE_URL;
  const apiKey = process.env.EVAL_API_KEY;
  const model = process.env.EVAL_MODEL;
  const missing = ["EVAL_BASE_URL", "EVAL_API_KEY", "EVAL_MODEL"].filter((k) => !process.env[k]);
  if (missing.length > 0) {
    throw new Error(
      `missing ${missing.join(", ")}. This script only calls an OpenAI-compatible endpoint; set all three, e.g.\n` +
        `  EVAL_BASE_URL=https://api.example.com/v1 EVAL_API_KEY=... EVAL_MODEL=... bun scripts/eval.mjs`,
    );
  }
  return { baseUrl: baseUrl.replace(/\/$/, ""), apiKey, model };
}

async function callModel(env, system, user, timeoutMs) {
  const started = Date.now();
  const res = await fetch(`${env.baseUrl}/chat/completions`, {
    method: "POST",
    signal: AbortSignal.timeout(timeoutMs),
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${env.apiKey}` },
    body: JSON.stringify({
      model: env.model,
      messages: [
        { role: "system", content: system },
        { role: "user", content: user },
      ],
      ...DEFAULT_PARAMS,
      stream: false,
    }),
  });
  const body = await res.text();
  const base = {
    model: env.model,
    params: { ...DEFAULT_PARAMS, stream: false },
    duration_ms: Date.now() - started,
    upstream_http_status: res.status,
  };
  if (!res.ok) {
    // An upstream error body can echo the prompt; keep the status, not the body.
    return { ...base, status: "failed", error_kind: "upstream", output: "", error: `upstream HTTP ${res.status}` };
  }
  let content = null;
  try {
    content = JSON.parse(body)?.choices?.[0]?.message?.content ?? null;
  } catch {
    content = null;
  }
  if (typeof content !== "string" || content === "") {
    return { ...base, status: "failed", error_kind: "invalid", output: "", error: "no usable choices[0].message.content", response_bytes: body.length };
  }
  return { ...base, status: "succeeded", error_kind: null, output: content, error: null };
}

async function loadSamples(path) {
  const text = await readFile(path, "utf8");
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map((line, i) => {
      let sample;
      try {
        sample = JSON.parse(line);
      } catch (e) {
        throw new Error(`${path}:${i + 1} is not valid JSON: ${e.message}`);
      }
      for (const field of ["id", "input", "original", "optimized"]) {
        if (typeof sample[field] !== "string" || sample[field] === "") {
          throw new Error(`${path}:${i + 1} (${sample.id ?? "?"}) missing non-empty "${field}"`);
        }
      }
      return sample;
    });
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const samplesPath = args.samples ?? "fixtures/eval-samples.jsonl";
  const outDir = args.out ?? "eval-out";
  const runs = Number(args.runs ?? DEFAULT_RUNS);
  if (!Number.isInteger(runs) || runs < 1) throw new Error("--runs must be a positive integer");
  const timeoutMs = Number(args.timeout ?? 120000);
  const random = makeRandom(args.seed);
  // One system message for both arms, from the environment or the documented default.
  const system =
    args.system ??
    process.env.EVAL_SYSTEM ??
    "你是一个执行任务的助手。直接完成任务并输出结果本身，不要复述或改写指令，不要输出解释性前言。";

  const env = configFromEnv();
  const samples = await loadSamples(samplesPath);

  console.log(`endpoint: ${env.baseUrl} model: ${env.model}`);
  console.log(`samples: ${samples.length} from ${samplesPath}`);
  console.log(`calls: ${samples.length} samples x ${ARMS.length} arms x ${runs} runs = ${samples.length * ARMS.length * runs}`);
  console.log(`system message: ${system}`);

  await mkdir(outDir, { recursive: true });
  const responsesPath = `${outDir}/responses.jsonl`;
  await writeFile(responsesPath, "");

  const records = [];
  for (const sample of samples) {
    for (const arm of ARMS) {
      const user = buildUserMessage(sample[arm], sample.input);
      for (let runIndex = 1; runIndex <= runs; runIndex += 1) {
        const startedAt = new Date().toISOString();
        let result;
        try {
          result = await callModel(env, system, user, timeoutMs);
        } catch (err) {
          // A transport failure is data: record it, count it, keep going. The
          // parameters we *sent* are still known, so they are recorded too —
          // otherwise one blip would look like the two arms differed.
          result = {
            model: env.model,
            params: { ...DEFAULT_PARAMS, stream: false },
            status: "failed",
            error_kind: "transport",
            output: "",
            error: err.name === "TimeoutError" ? `timed out after ${timeoutMs}ms` : err.message,
            duration_ms: null,
          };
        }
        const check = result.status === "succeeded" ? runChecks(result.output, sample.checks) : {
          scored: true,
          passed: false,
          outcomes: [{ name: "run_succeeded", passed: false, detail: result.error ?? "run failed" }],
        };
        const record = {
          sample_id: sample.id,
          arm,
          run_index: runIndex,
          started_at: startedAt,
          model: result.model,
          params: result.params,
          status: result.status,
          error_kind: result.error_kind,
          error: result.error,
          duration_ms: result.duration_ms,
          upstream_http_status: result.upstream_http_status,
          // The exact prompt sent, so a run can be reproduced verbatim.
          system,
          prompt_sent: user,
          prompt_template_chars: codepoints(sample[arm]),
          output: result.output,
          output_chars: codepoints(result.output),
          checks_scored: check.scored,
          checks_passed: check.passed,
          checks: check.outcomes,
        };
        records.push(record);
        // Append per run: a crash mid-suite keeps what already ran.
        await appendFile(responsesPath, JSON.stringify(record) + "\n");
        const mark = result.status === "succeeded" ? (check.passed === false ? "check-FAIL" : "ok") : `FAIL(${result.error_kind})`;
        console.log(`  ${sample.id} ${arm} #${runIndex}: ${mark}`);
      }
    }
  }

  // --- blind A/B -------------------------------------------------------------
  // Pair original run k with optimized run k. The sheet holds the two task
  // results and nothing else: no arm, no check outcome, no sample id.
  const blindItems = [];
  const mapping = { _how: "optimized_side tells which of a/b came from the optimized prompt.", seed: args.seed === undefined ? null : String(args.seed), items: {} };
  for (const sample of samples) {
    for (let runIndex = 1; runIndex <= runs; runIndex += 1) {
      const pick = (arm) => records.find((r) => r.sample_id === sample.id && r.arm === arm && r.run_index === runIndex);
      const original = pick("original");
      const optimized = pick("optimized");
      if (!original || !optimized) continue;
      const optimizedIsA = random() < 0.5;
      const itemId = `item-${String(blindItems.length + 1).padStart(3, "0")}`;
      blindItems.push({
        item_id: itemId,
        task_input: sample.input,
        rubric: sample.rubric ?? null,
        a: optimizedIsA ? optimized.output : original.output,
        b: optimizedIsA ? original.output : optimized.output,
      });
      mapping.items[itemId] = { sample_id: sample.id, run_index: runIndex, optimized_side: optimizedIsA ? "a" : "b" };
    }
  }

  const annotationTemplate = {
    _how: 'Set each value to "a", "b" or "tie". Leave null to skip. Then: bun scripts/eval.mjs --summarize eval-out --annotations <file>',
    items: Object.fromEntries(blindItems.map((i) => [i.item_id, null])),
  };

  await writeFile(`${outDir}/blind-items.jsonl`, blindItems.map((i) => JSON.stringify(i)).join("\n") + "\n");
  await writeFile(`${outDir}/blind-map.json`, JSON.stringify(mapping, null, 2) + "\n");
  await writeFile(`${outDir}/blind-annotations.template.json`, JSON.stringify(annotationTemplate, null, 2) + "\n");

  const summary = summarize(records, samples, runs, { endpoint: env.baseUrl, model: env.model });
  summary.blind.items = blindItems.length;
  summary.blind.unannotated = blindItems.length;
  await writeFile(`${outDir}/summary.json`, JSON.stringify(summary, null, 2) + "\n");

  console.log(`\noutput: ${outDir}/{responses.jsonl,blind-items.jsonl,blind-map.json,blind-annotations.template.json,summary.json}`);
  printSummary(summary);
  if (summary.failures === summary.calls) {
    // Nothing usable came back: report it as a failure instead of a clean run.
    console.error(`\neval: every one of the ${summary.calls} calls failed; see ${responsesPath}`);
    process.exitCode = 1;
    return;
  }
  console.log(`\nnext: fill ${outDir}/blind-annotations.template.json, then\n  bun scripts/eval.mjs --summarize ${outDir} --annotations ${outDir}/blind-annotations.json`);
}

function summarize(records, samples, runs, meta) {
  const summary = {
    generated_at: new Date().toISOString(),
    endpoint: meta.endpoint,
    model: meta.model,
    runs_per_arm: runs,
    samples: samples.length,
    calls: records.length,
    failures: records.filter((r) => r.status !== "succeeded").length,
    calls_by_sample_arm: {},
    checks: { scored_runs: 0, passed_runs: 0, failed_runs: 0, unscored_runs: 0, failing_checks: {} },
    per_sample: {},
    degenerate_samples: [],
    blind: { items: 0, annotated: 0, unannotated: 0, optimized_wins: 0, original_wins: 0, ties: 0 },
    params_consistent: null,
    params_fingerprints: [],
  };

  for (const r of records) {
    const key = `${r.sample_id}/${r.arm}`;
    summary.calls_by_sample_arm[key] = (summary.calls_by_sample_arm[key] ?? 0) + 1;
  }

  for (const r of records) {
    const slot = (summary.per_sample[`${r.sample_id}/${r.arm}`] ??= { runs: 0, passed: 0, failed: 0 });
    slot.runs += 1;
    if (!r.checks_scored) {
      summary.checks.unscored_runs += 1;
      continue;
    }
    summary.checks.scored_runs += 1;
    if (r.checks_passed) {
      summary.checks.passed_runs += 1;
      slot.passed += 1;
    } else {
      summary.checks.failed_runs += 1;
      slot.failed += 1;
      for (const o of r.checks) if (!o.passed) summary.checks.failing_checks[o.name] = (summary.checks.failing_checks[o.name] ?? 0) + 1;
    }
  }

  // A sample where the optimized arm passes fewer runs than the original is a
  // regression and is reported as one, not hidden inside an average.
  for (const sample of samples) {
    const o = summary.per_sample[`${sample.id}/original`];
    const p = summary.per_sample[`${sample.id}/optimized`];
    if (!o || !p) continue;
    if (p.passed < o.passed) {
      summary.degenerate_samples.push({
        sample_id: sample.id,
        original_passed_runs: o.passed,
        optimized_passed_runs: p.passed,
        note: "optimized passed fewer runs than original",
      });
    }
  }

  const fingerprints = new Set(records.map((r) => JSON.stringify({ model: r.model, params: r.params })));
  summary.params_fingerprints = [...fingerprints];
  summary.params_consistent = fingerprints.size <= 1;
  return summary;
}

// Offline: reads what the run already wrote and counts a human's A/B/tie votes.
// No model is called here, and nothing outside outDir is read: the sample ids and
// run counts come from responses.jsonl, so later edits to the sample file (or its
// deletion) cannot change a summary of a run that has already happened.
async function summarizeOnly(outDir, annotationsPath) {
  const sampleRecords = (await readFile(`${outDir}/responses.jsonl`, "utf8"))
    .split("\n")
    .filter((l) => l.trim() !== "")
    .map((l) => JSON.parse(l));
  if (sampleRecords.length === 0) throw new Error(`${outDir}/responses.jsonl has no records`);
  const mapping = JSON.parse(await readFile(`${outDir}/blind-map.json`, "utf8"));
  const annotations = annotationsPath ? JSON.parse(await readFile(annotationsPath, "utf8")) : { items: {} };
  const votes = annotations.items ?? {};

  // Only ids are needed: per-run check outcomes are already stored in the records.
  const samples = [...new Set(sampleRecords.map((r) => r.sample_id))].map((id) => ({ id }));
  const runs = Math.max(...sampleRecords.map((r) => r.run_index));
  const summary = summarize(sampleRecords, samples, runs, { endpoint: null, model: sampleRecords[0]?.model ?? null });

  const itemIds = Object.keys(mapping.items);
  summary.blind.items = itemIds.length;
  for (const itemId of itemIds) {
    const vote = votes[itemId];
    if (vote !== "a" && vote !== "b" && vote !== "tie") continue;
    summary.blind.annotated += 1;
    if (vote === "tie") summary.blind.ties += 1;
    else if (vote === mapping.items[itemId].optimized_side) summary.blind.optimized_wins += 1;
    else summary.blind.original_wins += 1;
  }
  summary.blind.unannotated = itemIds.length - summary.blind.annotated;
  summary.summarized_at = new Date().toISOString();
  return summary;
}

function printSummary(summary) {
  console.log(`calls: ${summary.calls} (${summary.failures} failed; failures are kept in responses.jsonl)`);
  if (summary.checks.scored_runs === 0) {
    console.log(`checks: no sample defines executable checks; ${summary.checks.unscored_runs} run(s) left for human A/B only (no automatic verdict)`);
  } else {
    console.log(`checks: ${summary.checks.passed_runs}/${summary.checks.scored_runs} scored runs passed all checks; unscored runs: ${summary.checks.unscored_runs}`);
  }
  if (Object.keys(summary.checks.failing_checks).length > 0) {
    console.log(`failing checks: ${JSON.stringify(summary.checks.failing_checks)}`);
  }
  console.log(`degenerate samples (optimized passed fewer runs than original): ${summary.degenerate_samples.length}`);
  for (const d of summary.degenerate_samples) {
    console.log(`  - ${d.sample_id}: original ${d.original_passed_runs} vs optimized ${d.optimized_passed_runs}`);
  }
  console.log(`params identical across all runs: ${summary.params_consistent}`);
  if (summary.blind.items > 0) {
    console.log(`blind A/B: ${summary.blind.items} pairs, annotated ${summary.blind.annotated} (unscored ${summary.blind.unannotated})`);
    if (summary.blind.annotated > 0) {
      console.log(`  optimized ${summary.blind.optimized_wins} wins, original ${summary.blind.original_wins} wins, ${summary.blind.ties} ties`);
    }
  }
}

// --- offline summary entry point ---------------------------------------------
const argv = process.argv.slice(2);
if (argv.includes("--summarize")) {
  const args = parseArgs(argv);
  const outDir = typeof args.summarize === "string" ? args.summarize : args.out ?? "eval-out";
  summarizeOnly(outDir, typeof args.annotations === "string" ? args.annotations : null)
    .then(async (summary) => {
      await writeFile(`${outDir}/summary.json`, JSON.stringify(summary, null, 2) + "\n");
      console.log(`wrote ${outDir}/summary.json (offline: no model calls)`);
      printSummary(summary);
    })
    .catch((err) => {
      console.error(`eval --summarize: ${err.message}`);
      process.exitCode = 1;
    });
} else {
  main().catch((err) => {
    console.error(`eval: ${err.message}`);
    process.exitCode = 1;
  });
}
