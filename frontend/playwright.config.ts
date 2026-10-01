import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 45000,
  use: {
    baseURL: "http://127.0.0.1:5175",
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
  },
  projects: [
    { name: "demo", testIgnore: /yufolo\.spec\.ts/ },
    {
      name: "yufolo",
      testMatch: /yufolo\.spec\.ts/,
      use: {
        baseURL: "http://127.0.0.1:5176",
        permissions: ["microphone"],
        launchOptions: {
          args: [
            "--use-fake-device-for-media-stream",
            "--use-fake-ui-for-media-stream",
          ],
        },
      },
    },
  ],
  webServer: [
    {
      command:
        "cd ../backend && go test -tags=e2e ./internal/app -run TestBrowserFixture -v -timeout 0",
      url: "http://127.0.0.1:18086/api/health",
      reuseExistingServer: false,
      timeout: 120000,
    },
    {
      command: "npm run dev -- --port 5176",
      env: { API_TARGET: "http://127.0.0.1:18086" },
      url: "http://127.0.0.1:5176",
      reuseExistingServer: false,
    },
    {
      command: "cd ../backend && go run ./cmd/server",
      env: {
        YUACTION_DEMO: "true",
        LISTEN_ADDR: "127.0.0.1:18084",
        YUFOLO_URL: "",
        DATABASE_URL: "",
        YUACTION_CREATOR_KEY: "",
        OPENAI_API_KEY: "",
        OPENAI_API_KEYs: "",
        OPENAI_EMBEDDING_API_KEY: "",
        OPENAI_MODEL: "",
      },
      url: "http://127.0.0.1:18084/api/health",
      reuseExistingServer: false,
      timeout: 120000,
    },
    {
      command: "npm run dev -- --port 5175",
      env: { API_TARGET: "http://127.0.0.1:18084" },
      url: "http://127.0.0.1:5175",
      reuseExistingServer: false,
    },
  ],
});
