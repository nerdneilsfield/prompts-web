// Demonstration import. Run it deliberately; the server never seeds anything
// on startup. Everything here is written through the public HTTP API and lands
// in SQLite like a user entry, tagged "演示" so it is recognizable afterwards.
//
//   bun scripts/import-demo.mjs --url http://127.0.0.1:8080 --password "$APP_PASSWORD"
//
// Prompts and schemes only. No optimization runs are created: a demo entry is
// not evidence about optimization quality, and runs are tied to a real model.

import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { login, parseArgs } from "./lib/client.mjs";

const DEMO_TAG = "演示";

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const url = args.url ?? process.env.APP_URL ?? "http://127.0.0.1:8080";
  const password = args.password ?? process.env.APP_PASSWORD;
  if (!password) {
    throw new Error("missing password: pass --password or set APP_PASSWORD (never commit it)");
  }
  const fixture = JSON.parse(await readFile(new URL("../fixtures/demo.json", import.meta.url), "utf8"));

  const { client, modelConfigured } = await login(url, password);
  console.log(`logged in to ${url} (model configured: ${modelConfigured})`);

  // Schemes first: prompts are standalone, but a scheme is what makes the
  // workbench usable immediately after import.
  const existingSchemes = await client.get("/api/schemes");
  const bySchemeName = new Map(existingSchemes.map((s) => [s.name, s]));
  const schemeIds = new Map();
  let schemesCreated = 0;
  for (const scheme of fixture.schemes) {
    if (bySchemeName.has(scheme.name)) {
      schemeIds.set(scheme.name, bySchemeName.get(scheme.name).id);
      console.log(`scheme kept (already present): ${scheme.name}`);
      continue;
    }
    const created = await client.post("/api/schemes", scheme);
    schemeIds.set(scheme.name, created.id);
    schemesCreated += 1;
    console.log(`scheme created: ${scheme.name} (id ${created.id})`);
  }

  const existingPrompts = await client.get("/api/prompts");
  const byTitle = new Map(existingPrompts.map((p) => [p.title, p]));
  let promptsCreated = 0;
  let promptsKept = 0;
  for (const prompt of fixture.prompts) {
    // Titles are the identity used here, so re-running the import is harmless.
    if (byTitle.has(prompt.title)) {
      promptsKept += 1;
      console.log(`prompt kept (already present): ${prompt.title}`);
      continue;
    }
    const tags = [...new Set([...(prompt.tags ?? []), DEMO_TAG])];
    const created = await client.post("/api/prompts", { ...prompt, tags });
    promptsCreated += 1;
    console.log(`prompt created: ${created.title} (id ${created.id}, tags ${created.tags.join("/")})`);
  }

  console.log(
    `done: ${promptsCreated} prompt(s) and ${schemesCreated} scheme(s) created, ` +
      `${promptsKept} prompt(s) already present. Everything imported is tagged "${DEMO_TAG}" and is clearly demo data.`,
  );
  console.log(`fixture: ${fileURLToPath(new URL("../fixtures/demo.json", import.meta.url))}`);
}

main().catch((err) => {
  console.error(`import-demo: ${err.message}`);
  process.exitCode = 1;
});
