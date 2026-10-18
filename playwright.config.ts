import { defineConfig } from "@playwright/test";

// Real Go server + SQLite in a throwaway dir, talking to the mock model.
export default defineConfig({
  testDir: "tests",
  workers: 1,
  timeout: 30_000,
  use: { baseURL: "http://127.0.0.1:18080" },
  projects: [{ name: "chromium", use: { browserName: "chromium", viewport: { width: 1280, height: 900 } } }],
  webServer: [
    { command: "node tests/mock-llm.mjs", port: 18081, reuseExistingServer: false },
    {
      command: "rm -rf test-results/data && go build -o bin/server ./cmd/server && ./bin/server",
      url: "http://127.0.0.1:18080/api/session",
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
        APP_PASSWORD: "secret",
        APP_ORIGIN: "http://127.0.0.1:18080",
        LISTEN_ADDR: "127.0.0.1:18080",
        DATA_DIR: "test-results/data",
        LLM_BASE_URL: "http://127.0.0.1:18081/v1",
        LLM_API_KEY: "test-key",
        LLM_MODEL: "mock-model",
      },
    },
  ],
});
