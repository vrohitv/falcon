// Reading code: Falcon's own selection, find in the file, tabs that remember their place, and the
// folder search. Selection is checked through the clipboard, which holds exactly what was selected.
import { test, expect, type Page } from "@playwright/test";
import { writeFileSync, mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";

const dir = join(import.meta.dirname, "..", ".e2e");
const mod = "Control"; // Falcon runs on Windows and Linux: every shortcut is Ctrl
// big.py: 20,000 functions of three lines each.
const big = Array.from({ length: 20000 }, (_, i) => `def function_${i}():\n    return ${i}\n`).join("\n") + "\n";
const bigLines = big.split("\n");

test.use({ permissions: ["clipboard-read", "clipboard-write"] });

async function repoIDs(page: Page) {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  return Object.fromEntries(repos.map((x: { name: string; id: string }) => [x.name, x.id])) as Record<string, string>;
}
function watch(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  return () => expect(errors, "page errors").toEqual([]);
}
async function copied(page: Page) {
  await page.evaluate(() => navigator.clipboard.writeText(""));
  await page.keyboard.press(`${mod}+c`);
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).not.toBe("");
  return page.evaluate(() => navigator.clipboard.readText());
}
const row = (page: Page, n: number) => page.locator(`.code-row[data-line="${n}"] .code-text`);

test("selection is Falcon's own: drag, drag past the edge, word, line, Shift, keys, select all", async ({ page }) => {
  const done = watch(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/#big.py`);
  await expect(row(page, 1)).toHaveText("def function_0():");
  const viewport = page.locator(".code-viewport");

  // A drag from the start of line 2 to the start of line 6.
  let a = (await row(page, 2).boundingBox())!,
    z = (await row(page, 6).boundingBox())!;
  await page.mouse.move(a.x + 1, a.y + 11);
  await page.mouse.down();
  await page.mouse.move(z.x + 1, z.y + 11, { steps: 8 });
  await page.mouse.up();
  expect(await copied(page)).toBe(bigLines.slice(1, 5).join("\n") + "\n");
  await expect(page.locator(".code-foot span").first()).toContainText("Ln 6");

  // A drag past the bottom edge scrolls and keeps where it started; the selection survives being
  // scrolled away (lines no longer on screen are still copied).
  a = (await row(page, 3).boundingBox())!;
  const box = (await viewport.boundingBox())!;
  await page.mouse.move(a.x + 1, a.y + 11);
  await page.mouse.down();
  await page.mouse.move(a.x + 100, box.y + box.height + 60, { steps: 5 });
  await expect.poll(() => viewport.evaluate((e) => e.scrollTop)).toBeGreaterThan(1500);
  await page.mouse.up();
  const dragged = await copied(page);
  expect(dragged.startsWith(bigLines[2] + "\n")).toBe(true);
  expect(dragged.split("\n").length).toBeGreaterThan(60);
  await viewport.evaluate((e) => (e.scrollTop = 0));
  expect(await copied(page)).toBe(dragged);

  // Double-click: the word; triple-click: the line; Shift-click: extend.
  await viewport.evaluate((e) => (e.scrollTop = 0));
  const d = (await row(page, 4).boundingBox())!;
  await page.mouse.dblclick(d.x + 10 * 7.8, d.y + 11); // inside "function_1" (line 4)
  expect(await copied(page)).toBe("function_1");
  a = (await row(page, 5).boundingBox())!;
  await page.mouse.click(a.x + 20, a.y + 11, { clickCount: 3 });
  expect(await copied(page)).toBe(bigLines[4] + "\n");
  await page.mouse.click(a.x + 1, a.y + 11);
  z = (await row(page, 7).boundingBox())!;
  await page.keyboard.down("Shift");
  await page.mouse.click(z.x + 1, z.y + 11);
  await page.keyboard.up("Shift");
  expect(await copied(page)).toBe(bigLines.slice(4, 6).join("\n") + "\n");

  // Keys: Escape clears; Shift+↓ selects lines; Ctrl+Shift+→ a word.
  await page.keyboard.press("Escape");
  await page.mouse.click(a.x + 1, a.y + 11);
  await page.keyboard.press("Shift+ArrowDown");
  await page.keyboard.press("Shift+ArrowDown");
  expect(await copied(page)).toBe(bigLines.slice(4, 6).join("\n") + "\n");
  await page.mouse.click(a.x + 1, a.y + 11);
  await page.keyboard.press("Control+Shift+ArrowRight");
  expect(await copied(page)).toBe("    return"); // spaces skipped, then the word, as in VS Code
  await page.keyboard.press("Control+Shift+ArrowRight");
  expect(await copied(page)).toBe("    return 1");

  // Select all copies the whole 60,000-line file.
  await page.keyboard.press(`${mod}+a`);
  expect(await copied(page)).toBe(big.replace(/\n$/, "") + "\n");
  done();
});

test("find in the file: every match, not just the ones on screen; Enter, Esc, F3", async ({ page }) => {
  const done = watch(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/#big.py`);
  await expect(row(page, 1)).toBeVisible();
  await page.locator(".code-viewport").click({ position: { x: 300, y: 40 } });
  await page.keyboard.press(`${mod}+f`);
  const input = page.getByLabel("Find in file");
  await input.fill("return 1999");
  // return 1999, 19990-19999: 11 matches, almost all far below the screen.
  await expect(page.locator(".find-count")).toHaveText("1 of 11");
  await page.getByRole("button", { name: "Whole word" }).click();
  await expect(page.locator(".find-count")).toHaveText("1 of 1");
  await page.getByRole("button", { name: "Whole word" }).click();
  await input.press("Enter");
  await expect(page.locator(".find-count")).toHaveText("2 of 11");
  await input.press("Shift+Enter");
  await input.press("Shift+Enter");
  await expect(page.locator(".find-count")).toHaveText("11 of 11");
  await expect(page.locator(".code-foot span").first()).toContainText(`Ln ${bigLines.indexOf("    return 19999") + 1}`);
  // Esc closes and leaves the match selected; F3 goes on to the next.
  await input.press("Escape");
  await expect(page.locator(".find-bar")).toHaveCount(0);
  expect(await copied(page)).toBe("return 1999"); // the match inside "return 19999"
  await page.keyboard.press("F3");
  await expect(page.locator(".code-foot span").first()).toContainText(`Ln ${bigLines.indexOf("    return 1999") + 1}`);
  // A bad regular expression says so instead of failing.
  await page.keyboard.press(`${mod}+f`);
  await page.getByRole("button", { name: "Regular expression" }).click();
  await input.fill("return (");
  await expect(page.locator(".find-count")).toHaveText("Invalid regular expression");
  done();
});

test("tabs: one strip that scrolls, same names show their folder, each tab keeps its place", async ({ page }) => {
  const done = watch(page);
  const extra = join(dir, "alpha/tools");
  mkdirSync(extra, { recursive: true });
  writeFileSync(join(extra, "main.go"), "package main\n\nfunc main() {}\n");
  try {
    const ids = await repoIDs(page);
    await page.goto(`/r/${ids.alpha}/#big.py:2000`);
    await expect(page.locator(".code-row.current")).toBeVisible();
    const viewport = page.locator(".code-viewport");
    const top = await viewport.evaluate((e) => e.scrollTop);
    for (const f of ["main.go", "tools/main.go", "notes.md", "go.mod"]) {
      await page.keyboard.press(`${mod}+p`);
      await page.getByRole("textbox", { name: "Go to file" }).fill(f);
      // The first result is exactly this file (its path is shown next to its name).
      await expect(page.locator(".palette-results > button").first().locator(".palette-path")).toHaveText(f);
      await page.keyboard.press("Enter");
      await expect(page.locator(".tab.active")).toContainText(f.split("/").at(-1)!);
    }
    // Two main.go tabs: the nested one says where it is (the one at the root has no folder to show).
    await expect(page.locator(".tab-dir")).toHaveText(["tools"]);
    // go.mod reads as a Go module, not another language.
    await expect(page.locator(".code-toolbar")).toContainText("Go Module");
    // Back to big.py: the same place, not line 1.
    await page.locator(".tab", { hasText: "big.py" }).getByRole("tab").click();
    await expect.poll(() => viewport.evaluate((e) => e.scrollTop)).toBe(top);
    // The actions stay at the right whatever the tabs.
    await expect(page.getByRole("button", { name: "Outline" })).toBeInViewport();
    // Close from the tab; the next one is shown.
    await page.locator(".tab", { hasText: "big.py" }).hover();
    await page.getByRole("button", { name: "Close big.py" }).click();
    await expect(page.locator(".tab", { hasText: "big.py" })).toHaveCount(0);
  } finally {
    rmSync(extra, { recursive: true, force: true });
  }
  done();
});

test("folder search: capitals do not change the case rule; a result opens with its match selected", async ({ page }) => {
  const done = watch(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await page.keyboard.press(`${mod}+Shift+f`);
  const box = page.getByLabel("Search the folder");
  await box.fill("HELLO");
  // greet.go: "Hello says hello", "func Hello", "hello " — any case while Match case is off.
  await expect(page.locator(".search-line").first()).toBeVisible();
  const any = await page.locator(".search-line").count();
  await page.getByRole("button", { name: "Match case" }).click();
  await expect(page.locator(".search-summary")).toContainText("0 results");
  await page.getByRole("button", { name: "Match case" }).click();
  await expect(page.locator(".search-line")).toHaveCount(any);
  expect(any).toBeGreaterThanOrEqual(3);
  // ↓ then Enter opens the second result with its match selected.
  await box.press("ArrowDown");
  await box.press("Enter");
  await expect(page.locator(".tab.active")).toBeVisible();
  await page.locator(".code-viewport").focus();
  expect((await copied(page)).toLowerCase()).toBe("hello");
  done();
});
