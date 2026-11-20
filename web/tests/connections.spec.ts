// Many Falcon tabs at once: each page holds one WebSocket (not an event stream per feature), so the
// browser's six connections per host are never used up and every tab stays responsive.
import { test, expect } from "@playwright/test";

test("eight tabs open on files, all responsive", async ({ context, page }) => {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  const alpha = repos.find((x: { name: string }) => x.name === "alpha").id;
  const pages = [page];
  for (let i = 1; i < 8; i++) pages.push(await context.newPage());
  for (const p of pages) {
    await p.goto(`/r/${alpha}/#main.go`);
    await expect(p.locator(".code-row").first()).toBeVisible({ timeout: 10000 });
  }
  // Every tab can still ask the server something and get an answer quickly.
  for (const p of pages) {
    const ms = await p.evaluate(async () => {
      const t = performance.now();
      await fetch(location.pathname.replace(/^\/r\/([^/]+)\/.*/, "/api/r/$1/summary"));
      return performance.now() - t;
    });
    expect(ms).toBeLessThan(2000);
  }
  // One socket per page, no event streams.
  const sockets = await page.evaluate(() => performance.getEntriesByType("resource").filter((e) => /\/(events|watch)(\?|$)/.test(e.name)).length);
  expect(sockets).toBe(0);
});
