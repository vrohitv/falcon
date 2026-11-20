// The workbench around the code: split-editor edge cases, the minimap, every Ctrl shortcut, and the same
// file open in two groups while one edits it.
import { test, expect, type Page } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const dir = join(import.meta.dirname, "..", ".e2e");
test.use({ permissions: ["clipboard-read", "clipboard-write"] });

async function repoIDs(page: Page) {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  return Object.fromEntries(repos.map((x: { name: string; id: string }) => [x.name, x.id])) as Record<string, string>;
}
const groups = (page: Page) => page.locator(".editor-cell");
async function fresh(page: Page, hash = "") {
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("falcon.layout:")).forEach((k) => localStorage.removeItem(k)));
  await page.goto(`/r/${ids.alpha}/${hash}`);
  return ids;
}
async function goTo(page: Page, file: string) {
  await page.keyboard.press("Control+p");
  await page.getByRole("textbox", { name: "Go to file" }).fill(file);
  await expect(page.locator(".palette-results > button").first().locator(".palette-path")).toHaveText(file);
  await page.keyboard.press("Enter");
}

test("split edge cases: an empty group closes, a drop on its own group is nothing, groups stack on a phone", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await fresh(page);
  await expect(page.locator(".file-empty")).toBeVisible();
  // Splitting with nothing open makes an empty group, which its own button closes.
  await page.keyboard.press("Control+\\");
  await expect(groups(page)).toHaveCount(2);
  await groups(page).nth(1).getByRole("button", { name: "Close group" }).click();
  await expect(groups(page)).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Close group" })).toHaveCount(0);
  // Two tabs; a tab dropped on its own group's centre stays where it was.
  await goTo(page, "main.go");
  await goTo(page, "go.mod");
  await page.locator(".tab", { hasText: "main.go" }).dragTo(page.locator(".source-body"));
  await expect(page.locator(".tab .tab-name")).toHaveText(["main.go", "go.mod"]);
  await expect(groups(page)).toHaveCount(1);
  // Close group closes its tabs too.
  await page.locator(".code-viewport").click({ position: { x: 200, y: 30 } });
  await page.keyboard.press("Control+\\");
  await expect(groups(page)).toHaveCount(2);
  await groups(page).nth(1).getByRole("button", { name: "Close group" }).click();
  await expect(groups(page)).toHaveCount(1);
  // On a phone the groups stack, each the full width.
  await page.keyboard.press("Control+\\");
  await page.setViewportSize({ width: 390, height: 780 });
  const a = (await groups(page).nth(0).boundingBox())!,
    b = (await groups(page).nth(1).boundingBox())!;
  expect(b.y).toBeGreaterThan(a.y + 50);
  expect(Math.abs(a.width - b.width)).toBeLessThan(2);
  expect(errors).toEqual([]);
});

test("the minimap: the file's shape, a box that follows, click and drag to scroll, find marks", async ({ page }) => {
  await fresh(page, "#big.py");
  const viewport = page.locator(".code-viewport");
  const map = page.locator(".minimap");
  await expect(map).toBeVisible();
  expect((await map.boundingBox())!.width).toBeLessThanOrEqual(50);
  // The shape is drawn: the canvas holds non-transparent pixels.
  await expect.poll(() => page.locator(".minimap canvas").evaluate((c: HTMLCanvasElement) => {
    const d = c.getContext("2d")!.getImageData(0, 0, c.width, Math.min(c.height, 200)).data;
    let n = 0;
    for (let i = 3; i < d.length; i += 4) if (d[i] > 0) n++;
    return n;
  })).toBeGreaterThan(50);
  // Click near the bottom: the end of the file.
  const m = (await map.boundingBox())!;
  await page.mouse.click(m.x + 20, m.y + m.height - 3);
  await expect.poll(() => viewport.evaluate((e) => e.scrollTop / (e.scrollHeight - e.clientHeight))).toBeGreaterThan(0.9);
  // Drag the box to the top.
  const box = (await page.locator(".minimap-box").boundingBox())!;
  await page.mouse.move(box.x + 10, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + 10, m.y + 1, { steps: 6 });
  await page.mouse.up();
  // One minimap pixel stands for many lines of a 60,000-line file: the top is within a pixel's worth.
  await expect.poll(() => viewport.evaluate((e) => e.scrollTop / (e.scrollHeight - e.clientHeight))).toBeLessThan(0.01);
  // The box follows the code's scrolling.
  const top = (await page.locator(".minimap-box").boundingBox())!.y;
  await viewport.evaluate((e) => (e.scrollTop = e.scrollHeight / 2));
  await expect.poll(async () => (await page.locator(".minimap-box").boundingBox())!.y).toBeGreaterThan(top + 50);
});

test("every shortcut is Ctrl (or Alt); the Command key does nothing", async ({ page }) => {
  await fresh(page, "#main.go");
  await expect(page.locator(".code-row").first()).toBeVisible();
  await page.locator(".code-viewport").click({ position: { x: 300, y: 30 } });
  await page.keyboard.press("Meta+p");
  await expect(page.locator(".palette")).toHaveCount(0);
  await page.keyboard.press("Control+p");
  await expect(page.getByRole("dialog", { name: "Go to file" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+Shift+o");
  await expect(page.getByRole("textbox", { name: "Go to file" })).toHaveValue("#");
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog", { name: "Switch folder" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+Shift+f");
  await expect(page.getByLabel("Search the folder")).toBeVisible();
  await page.keyboard.press("Control+Shift+e");
  await expect(page.getByLabel("Filter files")).toBeVisible();
  await page.keyboard.press("Control+b");
  await expect(page.locator(".file-sidebar")).toHaveCount(0);
  await page.keyboard.press("Control+b");
  await expect(page.locator(".file-sidebar")).toHaveCount(1);
  await page.locator(".code-viewport").click({ position: { x: 300, y: 30 } });
  await page.keyboard.press("Control+f");
  await expect(page.locator(".find-bar")).toBeVisible();
  await page.keyboard.press("Escape");
  // Labels say Ctrl, never ⌘.
  expect(await page.locator("body").evaluate((b) => /[⌘⌥⌃]/.test(b.innerText + [...b.querySelectorAll("[title]")].map((e) => e.getAttribute("title")).join(" ")))).toBe(false);
});

test("the same file in two groups: one edits and saves (Ctrl+S), the other shows it", async ({ page }) => {
  const notes = join(dir, "alpha/notes.md");
  const before = readFileSync(notes, "utf8");
  try {
    await fresh(page, "#notes.md");
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(page.locator(".code-row").first()).toBeVisible();
    await page.locator(".code-viewport").click({ position: { x: 300, y: 30 } });
    await page.keyboard.press("Control+\\");
    await expect(groups(page)).toHaveCount(2);
    await groups(page).nth(1).getByRole("button", { name: "Source", exact: true }).click();
    await page.keyboard.press("Control+1");
    await page.keyboard.press("Control+Shift+Enter");
    const editor = groups(page).nth(0).locator("textarea");
    await expect(editor).toBeVisible();
    await expect(groups(page).nth(1).locator("textarea")).toHaveCount(0);
    await editor.click();
    await editor.press("End");
    await page.keyboard.type(" (edited)");
    await page.keyboard.press("Control+s");
    await expect.poll(() => readFileSync(notes, "utf8")).toContain("(edited)");
    await expect(groups(page).nth(1).locator(".code-rows")).toContainText("(edited)");
  } finally {
    writeFileSync(notes, before);
  }
});
