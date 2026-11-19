import { test, expect, type Page } from "@playwright/test";
import { writeFileSync, readFileSync, appendFileSync } from "node:fs";
import { execSync } from "node:child_process";
import { join } from "node:path";

const dir = join(import.meta.dirname, "..", ".e2e");
const mod = process.platform === "darwin" ? "Meta" : "Control";

// Falcon must never reach beyond itself.
function offline(page: Page) {
  const outside: string[] = [];
  page.on("request", (r) => {
    const host = new URL(r.url()).host;
    if (host !== "127.0.0.1:7733") outside.push(r.url());
  });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  return () => {
    expect(outside, "requests outside Falcon").toEqual([]);
    expect(errors, "page errors").toEqual([]);
  };
}

async function repoIDs(page: Page) {
  const r = await page.request.get("/api/repos");
  const { repos } = await r.json();
  return Object.fromEntries(repos.map((x: { name: string; id: string }) => [x.name, x.id])) as Record<string, string>;
}

test("one server, a folder per tab, switched from the start page and ⌘K", async ({ page, context }) => {
  const done = offline(page);
  const ids = await repoIDs(page);
  await page.goto("/");
  await page.evaluate(() => localStorage.clear());
  await page.goto("/");
  await expect(page.locator(".welcome-folder")).toHaveCount(3);
  await page.locator(".welcome-folder", { hasText: "alpha" }).click();
  await expect(page).toHaveURL(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await expect(page).toHaveTitle("alpha · Falcon");
  // ⌘K switches this tab.
  await page.keyboard.press(`${mod}+k`);
  await page.getByRole("textbox", { name: "Switch folder" }).fill("beta");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(`/r/${ids.beta}/`);
  await expect(page.getByRole("treeitem", { name: "app.py" })).toBeVisible();
  // Another tab on the other folder, independently.
  const other = await context.newPage();
  await other.goto(`/r/${ids.alpha}/`);
  await expect(other.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await expect(page.getByRole("treeitem", { name: "app.py" })).toBeVisible();
  // Back goes to the previous folder.
  await page.goBack();
  await expect(page).toHaveURL(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await other.close();
  done();
});

test("go to file, a link to the line, and ⌘-click to the definition", async ({ page }) => {
  const done = offline(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await page.keyboard.press(`${mod}+p`);
  await page.getByRole("textbox", { name: "Go to file" }).fill("main.go");
  await page.keyboard.press("Enter");
  await expect(page.locator(".code-row").first()).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/r/${ids.alpha}/#main.go`));
  // ⌘-click Hello: it lives in another file.
  await expect(page.locator(".code-text", { hasText: "greet.Hello" })).toBeVisible();
  const point = await page.evaluate(() => {
    for (const row of document.querySelectorAll(".code-text")) {
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
      let n: Node | null;
      while ((n = walker.nextNode())) {
        const i = n.textContent!.indexOf("Hello");
        if (i >= 0 && row.textContent!.includes("greet.Hello")) {
          const r = document.createRange();
          r.setStart(n, i + 2);
          r.setEnd(n, i + 3);
          const b = r.getBoundingClientRect();
          return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
        }
      }
    }
    return null;
  });
  await page.keyboard.down(mod);
  await page.mouse.click(point!.x, point!.y);
  await page.keyboard.up(mod);
  await expect(page).toHaveURL(/#internal\/greet\/greet\.go:4/);
  await expect(page.locator(".code-row.current")).toContainText("func Hello");
  // Back returns to where the jump started.
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(page).toHaveURL(/#main\.go/);
  // A link with a line opens there after a reload.
  await page.goto(`/r/${ids.alpha}/#big.py:30001`);
  await expect(page.locator(".code-row.current")).toContainText("function_10000");
  done();
});

test("go to symbol finds a declaration anywhere in the folder", async ({ page }) => {
  const done = offline(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await page.keyboard.press(`${mod}+t`);
  await page.getByRole("textbox", { name: "Go to file" }).fill("#function_123");
  await expect(page.locator(".palette-results button").first()).toContainText("function_123", { timeout: 8000 });
  await expect(page.locator(".palette-results button").first()).toContainText("big.py:");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/#big\.py:\d+/);
  await expect(page.locator(".code-row.current")).toContainText("def function_123");
  done();
});

test("search the folder as you type, open a result at its line", async ({ page }) => {
  const done = offline(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await page.keyboard.press(`${mod}+Shift+f`);
  await page.getByLabel("Search the folder").fill("hello");
  await expect(page.locator(".search-summary")).toContainText(/\d+ results? in 2 files/);
  await page.getByLabel("Match case").click();
  await expect(page.locator(".search-summary")).toContainText("in 1 file");
  await page.locator(".search-line").first().click();
  await expect(page).toHaveURL(/#internal\/greet\/greet\.go:3/);
  await page.getByLabel("Match case").click();
  await page.getByLabel("Files to include").fill("*.md");
  await page.getByLabel("Search the folder").fill("repository");
  await expect(page.locator(".search-file")).toHaveCount(1);
  await expect(page.locator(".search-file")).toContainText("notes.md");
  done();
});

test("a 60,000-line file opens and scrolls without loading it all", async ({ page }) => {
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/#big.py`);
  await expect(page.locator(".code-row").first()).toBeVisible();
  const rows = await page.locator(".code-row").count();
  expect(rows).toBeLessThan(200);
  const t = Date.now();
  await page.goto(`/r/${ids.alpha}/#big.py:55000`);
  await expect(page.locator(".code-row.current")).toBeVisible();
  expect(Date.now() - t).toBeLessThan(3000);
});

test("edit and save; a change made on disk meanwhile is never overwritten", async ({ page }) => {
  const done = offline(page);
  const ids = await repoIDs(page);
  // Whatever happens, beta's app.py goes back to what it was.
  const restore = () => execSync("git checkout -q -- app.py", { cwd: join(dir, "beta") });
  page.on("close", () => {
    try {
      restore();
    } catch {}
  });
  await page.goto(`/r/${ids.beta}/#app.py`);
  await page.getByLabel("Edit file").click();
  const editor = page.getByLabel("Edit app.py");
  await editor.click();
  await editor.press(`${mod}+End`);
  await editor.type("\n# edited in Falcon\n");
  await page.keyboard.press(`${mod}+s`);
  await expect.poll(() => readFileSync(join(dir, "beta/app.py"), "utf8")).toContain("# edited in Falcon");
  // Edit again, but another tool writes the file first.
  await editor.type("# second edit\n");
  appendFileSync(join(dir, "beta/app.py"), "# written by another tool\n");
  await page.waitForTimeout(400);
  await page.keyboard.press(`${mod}+s`);
  await page.waitForTimeout(600);
  const disk = readFileSync(join(dir, "beta/app.py"), "utf8");
  expect(disk).toContain("# written by another tool");
  expect(disk).not.toContain("# second edit");
  execSync("git checkout -q -- app.py", { cwd: join(dir, "beta") });
  done();
});

// Puts alpha (and its folder remote) back exactly as the setup left it, so tests can run in any order
// and any number of times against one server.
function restoreAlpha(head: string) {
  const sh = (cmd: string, cwd = join(dir, "alpha")) => {
    try {
      execSync(cmd, { cwd, stdio: "ignore" });
    } catch {}
  };
  sh(`git reset -q --hard ${head}`);
  sh("git clean -fdq");
  sh("git branch --unset-upstream");
  sh("git update-ref -d refs/remotes/origin/main");
  sh("git update-ref -d refs/heads/main", join(dir, "remote.git"));
}

test("Source Control: a change from outside shows up live; stage a hunk, commit, publish", async ({ page }) => {
  const head = execSync("git rev-parse HEAD", { cwd: join(dir, "alpha") }).toString().trim();
  try {
    await sourceControlRound(page);
  } finally {
    restoreAlpha(head);
  }
});

async function sourceControlRound(page: Page) {
  const done = offline(page);
  const ids = await repoIDs(page);
  await page.goto(`/r/${ids.alpha}/`);
  await expect(page.getByRole("treeitem", { name: "main.go" })).toBeVisible();
  await expect(page.locator(".activity-badge")).toHaveCount(0);
  // Another terminal edits two places and adds a file; Falcon notices without a reload.
  const greet = join(dir, "alpha/internal/greet/greet.go");
  writeFileSync(greet, readFileSync(greet, "utf8").replace("hello ", "hi ").replace("// Hello says hello.", "// Hello greets."));
  writeFileSync(join(dir, "alpha/extra.txt"), "new file\n");
  await expect(page.locator(".activity-badge")).toHaveText("2", { timeout: 8000 });
  await expect(page.getByRole("treeitem", { name: "extra.txt" })).toBeVisible();
  await page.getByLabel("Source Control").click();
  await page.getByRole("treeitem", { name: "internal/greet/greet.go", exact: true }).locator("button").first().click();
  await expect(page.locator(".diff-hunk")).toHaveCount(1);
  await page.getByRole("button", { name: "Stage hunk" }).click();
  await expect(page.locator(".git-group-heading", { hasText: "Staged changes" })).toBeVisible();
  // Discard the new file from its row, confirmed.
  await page.getByLabel("Discard changes to extra.txt").click();
  await page.getByRole("button", { name: "Delete file" }).click();
  await expect(page.getByLabel("Discard changes to extra.txt")).toHaveCount(0);
  await page.getByLabel("Commit message").fill("Greet more briefly");
  await page.keyboard.press(`${mod}+Enter`);
  await expect(page.getByText("Working tree clean")).toBeVisible();
  await expect(page.locator(".activity-badge")).toHaveCount(0);
  // Publish to the folder remote (offline).
  await page.getByRole("button", { name: "Publish branch" }).first().click();
  await expect(page.locator(".git-tracking")).toBeVisible();
  const remote = execSync("git log --oneline -1 main", { cwd: join(dir, "remote.git") }).toString();
  expect(remote).toContain("Greet more briefly");
  done();
}

test("Source Control: rapid stage and unstage stay in order; Commit & Push", async ({ page }) => {
  const head = execSync("git rev-parse HEAD", { cwd: join(dir, "alpha") }).toString().trim();
  try {
    const done = offline(page);
    const ids = await repoIDs(page);
    for (const n of ["one", "two", "three"]) writeFileSync(join(dir, `alpha/${n}.txt`), n + "\n");
    await page.goto(`/r/${ids.alpha}/`);
    await page.getByLabel("Source Control").click();
    await expect(page.getByRole("button", { name: "Stage three.txt" })).toBeVisible({ timeout: 8000 });
    // Clicks in quick succession: each file moves at once, nothing is lost or reordered.
    let posts = 0;
    page.on("request", (r) => r.url().includes("/git/commands") && posts++);
    for (const n of ["one", "two", "three"]) await page.getByRole("button", { name: `Stage ${n}.txt` }).click();
    await page.getByRole("button", { name: "Unstage two.txt" }).click();
    await expect(page.getByRole("button", { name: "Unstage one.txt" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Stage two.txt" })).toBeVisible();
    await expect.poll(() => posts).toBe(4);
    await expect
      .poll(() => execSync("git diff --cached --name-only", { cwd: join(dir, "alpha") }).toString().trim().split("\n").sort().join(" "))
      .toBe("one.txt three.txt");
    // Commit & Push from the commit button's menu: no upstream yet, so it publishes the branch.
    await page.getByLabel("Commit message").fill("Add one and three");
    await page.getByRole("button", { name: "More commit actions" }).click();
    await page.getByRole("menuitem", { name: /Commit & Publish branch/ }).click();
    await expect(page.locator(".git-tracking")).toBeVisible();
    expect(execSync("git log --oneline -1 main", { cwd: join(dir, "remote.git") }).toString()).toContain("Add one and three");
    // Now tracked: ⌘⇧Enter commits and pushes.
    await page.getByRole("button", { name: "Stage two.txt" }).click();
    await page.getByLabel("Commit message").fill("Add two");
    await page.getByLabel("Commit message").press(`${mod}+Shift+Enter`);
    await expect.poll(() => execSync("git log --oneline -1 main", { cwd: join(dir, "remote.git") }).toString()).toContain("Add two");
    await expect(page.getByText("Working tree clean")).toBeVisible();
    done();
  } finally {
    restoreAlpha(head);
  }
});
