// Split editors and tab organisation, as in VS Code: groups side by side and stacked, tabs dragged
// between them or onto an edge to split, the tab menu, and a layout remembered per folder.
import { test, expect, type Page } from "@playwright/test";

async function repoIDs(page: Page) {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  return Object.fromEntries(repos.map((x: { name: string; id: string }) => [x.name, x.id])) as Record<string, string>;
}
const groupsOf = (page: Page) => page.locator(".editor-cell");
const tabsIn = (page: Page, group: number) => groupsOf(page).nth(group).locator(".tab .tab-name");

async function goTo(page: Page, file: string) {
  await page.keyboard.press("Control+p");
  await page.getByRole("textbox", { name: "Go to file" }).fill(file);
  await expect(page.locator(".palette-results > button").first().locator(".palette-path")).toHaveText(file);
  await page.keyboard.press("Enter");
}

test("split editors: Ctrl+\\, focus, drag tabs between groups and onto edges, the tab menu, a remembered layout", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  // A clean layout for this test.
  await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("falcon.layout:") || k.startsWith("falcon.tabs:")).forEach((k) => localStorage.removeItem(k)));
  await page.goto(`/r/${ids.alpha}/#main.go`);
  await expect(page.locator(".code-row").first()).toBeVisible();
  await expect(groupsOf(page)).toHaveCount(1);

  // Ctrl+\ splits right, showing the same file in the new (focused) group.
  await page.locator(".code-viewport").first().click({ position: { x: 200, y: 30 } });
  await page.keyboard.press("Control+\\");
  await expect(groupsOf(page)).toHaveCount(2);
  await expect(tabsIn(page, 1)).toHaveText(["main.go"]);
  await expect(groupsOf(page).nth(1).locator(".source-panel")).toHaveClass(/active-group/);
  // The explorer and go to file open into the focused group.
  await goTo(page, "notes.md");
  await expect(tabsIn(page, 1)).toHaveText(["main.go", "notes.md"]);
  await expect(tabsIn(page, 0)).toHaveText(["main.go"]);
  // Ctrl+1 focuses the first group; a file opened now goes there.
  await page.keyboard.press("Control+1");
  await expect(groupsOf(page).nth(0).locator(".source-panel")).toHaveClass(/active-group/);
  await goTo(page, "go.mod");
  await expect(tabsIn(page, 0)).toHaveText(["main.go", "go.mod"]);

  // Drag notes.md from the second group onto the first group's tab strip, before main.go.
  await groupsOf(page).nth(1).locator(".tab", { hasText: "notes.md" }).dragTo(groupsOf(page).nth(0).locator(".tab", { hasText: "main.go" }), { targetPosition: { x: 4, y: 10 } });
  await expect(tabsIn(page, 0)).toHaveText(["notes.md", "main.go", "go.mod"]);
  await expect(tabsIn(page, 1)).toHaveText(["main.go"]);

  // Dragging the second group's last tab away empties it: the group closes.
  await groupsOf(page).nth(1).locator(".tab", { hasText: "main.go" }).dragTo(groupsOf(page).nth(0).locator(".tab-strip"));
  await expect(groupsOf(page)).toHaveCount(1);
  await expect(tabsIn(page, 0)).toHaveText(["notes.md", "main.go", "go.mod"]);

  // Drag go.mod onto the bottom edge of its group: a new group below holds it.
  const body = groupsOf(page).nth(0).locator(".source-body");
  const box = (await body.boundingBox())!;
  await groupsOf(page).nth(0).locator(".tab", { hasText: "go.mod" }).dragTo(body, { targetPosition: { x: box.width / 2, y: box.height - 12 } });
  await expect(groupsOf(page)).toHaveCount(2);
  await expect(page.locator(".editor-column")).toHaveCount(1);
  await expect(tabsIn(page, 1)).toHaveText(["go.mod"]);
  await expect(tabsIn(page, 0)).toHaveText(["notes.md", "main.go"]);

  // The tab menu: Split Right, then Close Others.
  await groupsOf(page).nth(0).locator(".tab", { hasText: "main.go" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Split Right" }).click();
  await expect(page.locator(".editor-column")).toHaveCount(2);
  await expect(groupsOf(page)).toHaveCount(3);
  await groupsOf(page).nth(0).locator(".tab", { hasText: "main.go" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close Others" }).click();
  await expect(tabsIn(page, 0)).toHaveText(["main.go"]);

  // The line between the columns resizes them.
  const sash = page.locator(".grid-sash.vertical").first();
  const s = (await sash.boundingBox())!;
  const before = (await page.locator(".editor-column").first().boundingBox())!.width;
  await page.mouse.move(s.x + 1, s.y + s.height / 2);
  await page.mouse.down();
  await page.mouse.move(s.x - 150, s.y + s.height / 2, { steps: 5 });
  await page.mouse.up();
  const after = (await page.locator(".editor-column").first().boundingBox())!.width;
  expect(before - after).toBeGreaterThan(100);

  // The layout comes back after a reload.
  await page.reload();
  await expect(groupsOf(page)).toHaveCount(3);
  await expect(tabsIn(page, 0)).toHaveText(["main.go"]);
  await expect(tabsIn(page, 1)).toHaveText(["go.mod"]);
  await expect(tabsIn(page, 2)).toHaveText(["main.go"]);
  expect(errors).toEqual([]);
  await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("falcon.layout:")).forEach((k) => localStorage.removeItem(k)));
});
