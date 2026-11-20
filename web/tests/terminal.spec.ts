// The terminal panel: a real shell in the folder, Ctrl+` to show and hide, split and tabs, Ctrl+click on
// a path:line in the output, a page reload that comes back to running terminals with their output, and
// a lot of output at once.
import { test, expect, type Page } from "@playwright/test";

async function repoIDs(page: Page) {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  return Object.fromEntries(repos.map((x: { name: string; id: string }) => [x.name, x.id])) as Record<string, string>;
}
// The screen is drawn on a canvas: its text comes from the terminal's buffer.
const screen = (page: Page, pane = 0) =>
  page.evaluate((pane) => {
    const host = [...document.querySelectorAll(".terminal-tab.shown .term-host")][pane] as any;
    const t = host?.falconTerminal;
    if (!t) return "";
    const b = t.buffer.active;
    const out: string[] = [];
    for (let y = 0; y < b.length; y++) out.push(b.getLine(y)?.translateToString(true) ?? "");
    return out.join("\n");
  }, pane);
const cellOf = (page: Page, line: string) =>
  page.evaluate((line) => {
    const host = document.querySelector(".terminal-tab.shown .term-host") as any;
    const t = host.falconTerminal,
      b = t.buffer.active;
    for (let y = b.viewportY; y < b.viewportY + t.rows; y++)
      if ((b.getLine(y)?.translateToString(true) ?? "").trim() === line) {
        const s = host.querySelector(".xterm-screen").getBoundingClientRect();
        return { x: s.left + 5 * (s.width / t.cols), y: s.top + (y - b.viewportY + 0.5) * (s.height / t.rows) };
      }
    return null;
  }, line);

test("a shell in the folder: run, open a path from the output, split, tabs, hide, reload, kill", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("falcon.terminal")).forEach((k) => localStorage.removeItem(k)));
  await page.goto(`/r/${ids.alpha}/#go.mod`);
  await expect(page.locator(".code-row").first()).toBeVisible();

  // Ctrl+` opens the panel with a terminal, focused.
  await page.keyboard.press("Control+Backquote");
  await expect(page.locator(".terminal-tab.shown .xterm")).toHaveCount(1);
  await expect.poll(() => screen(page)).toMatch(/\S/);
  await page.keyboard.type("echo sum-$((40+2)); pwd; echo internal/greet/greet.go:4\n");
  await expect.poll(() => screen(page)).toContain("sum-42");
  expect(await screen(page)).toMatch(/alpha\s*$|\/alpha\n/m);

  // Ctrl+click a path:line in the output: it opens in the editor at that line.
  const at = (await cellOf(page, "internal/greet/greet.go:4"))!;
  await page.mouse.move(at.x, at.y);
  await page.keyboard.down("Control");
  await page.mouse.click(at.x, at.y);
  await page.keyboard.up("Control");
  await expect(page).toHaveURL(/#internal\/greet\/greet\.go:4/);
  await expect(page.locator(".code-foot span").first()).toContainText("Ln 4");

  // Falcon's own shortcuts still work with the terminal focused.
  await page.locator(".terminal-tab.shown .xterm").click();
  await page.keyboard.press("Control+p");
  await expect(page.getByRole("dialog", { name: "Go to file" })).toBeVisible();
  await page.keyboard.press("Escape");

  // A lot of output at once arrives whole (it is acknowledged as it is drawn).
  await page.locator(".terminal-tab.shown .xterm").click();
  await page.keyboard.type("seq 1 100000 | tail -n 3; seq 1 100000 > /dev/null; seq 1 50000; echo end-$((1+1))\n");
  await expect.poll(() => screen(page), { timeout: 30000 }).toContain("end-2");
  expect(await screen(page)).toContain("50000");

  // Split, then a second tab.
  await page.getByRole("button", { name: "Split terminal" }).click();
  await expect(page.locator(".terminal-tab.shown .terminal-pane")).toHaveCount(2);
  await page.getByRole("button", { name: "New terminal" }).click();
  await expect(page.locator(".terminal-tabs button")).toHaveCount(2);

  // Hidden, the terminals keep running; Ctrl+` brings them back.
  await page.keyboard.press("Control+Backquote");
  await expect(page.locator(".terminal-panel")).toHaveCount(0);
  const list = await (await page.request.get(`/api/r/${ids.alpha}/terminals`)).json();
  expect(list.sessions.length).toBe(3);

  // A reload comes back to the same terminals (the split kept), with their output.
  await page.keyboard.press("Control+Backquote");
  await expect(page.locator(".terminal-tabs button")).toHaveCount(2);
  await page.reload();
  await expect(page.locator(".terminal-tabs button")).toHaveCount(2);
  await page.locator(".terminal-tabs button").first().click();
  await expect(page.locator(".terminal-tab.shown .terminal-pane")).toHaveCount(2);
  // (The terminal keeps 10,000 lines: the latest output, not what scrolled past before it.)
  await expect.poll(() => screen(page)).toContain("end-2");

  // Kill ends the shells; the last one closes the panel.
  for (let i = 0; i < 3; i++) await page.getByRole("button", { name: "Kill terminal" }).click();
  await expect(page.locator(".terminal-panel")).toHaveCount(0);
  await expect.poll(async () => (await (await page.request.get(`/api/r/${ids.alpha}/terminals`)).json()).sessions.length).toBe(0);
  expect(errors).toEqual([]);
});
