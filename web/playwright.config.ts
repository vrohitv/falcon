import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  workers: 1,
  timeout: 30000,
  use: { baseURL: "http://127.0.0.1:7733", viewport: { width: 1440, height: 900 }, trace: "retain-on-failure" },
  webServer: { command: "bash ../scripts/e2e-server.sh", url: "http://127.0.0.1:7733/healthz", timeout: 180000, reuseExistingServer: false, env: {} },
  reporter: "list",
});
