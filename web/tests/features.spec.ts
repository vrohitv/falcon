import { test, expect, type Page } from "@playwright/test";
import { writeFileSync, readFileSync } from "node:fs";
import { execSync } from "node:child_process";
import { join } from "node:path";

const dir = join(import.meta.dirname, "..", ".e2e");
const gamma = join(dir, "gamma");
const mod = process.platform === "darwin" ? "Meta" : "Control";

function watch(page: Page) {
  const outside: string[] = [];
  const errors: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).host !== "127.0.0.1:7733") outside.push(r.url());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => {
    errors.push("dialog: " + d.message());
    void d.dismiss();
  });
  return () => {
    expect(outside, "requests outside Falcon").toEqual([]);
    expect(errors, "page errors").toEqual([]);
  };
}
async function gammaID(page: Page) {
  const { repos } = await (await page.request.get("/api/repos")).json();
  return repos.find((r: { name: string }) => r.name === "gamma").id as string;
}
const git = (args: string) => execSync("git " + args, { cwd: gamma }).toString();

test.describe("viewers", () => {
  test("Markdown renders with local images, offline badges, file links and anchors", async ({ page }) => {
    const done = watch(page);
    const id = await gammaID(page);
    await page.goto(`/r/${id}/#README.md`);
    const doc = page.locator(".markdown-doc");
    await expect(doc.locator("h1")).toHaveText("Gamma");
    // Local images load from the folder; the SVG keeps its written width.
    await expect.poll(() => doc.locator('img[alt="picture"]').evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(64);
    await expect(doc.locator('img[alt="logo"]')).toHaveAttribute("width", "40");
    // A web image is not fetched: its text stands in.
    await expect(doc.locator(".md-img-off")).toHaveText("badge");
    // Code blocks are highlighted, task lists ticked.
    await expect(doc.locator("pre.md-code code .k").first()).toHaveText("func");
    await expect(doc.locator(".md-check.done")).toHaveCount(1);
    // Source shows the text; Preview comes back.
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(page.locator(".code-text").first()).toContainText("# Gamma");
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(doc.locator("h1")).toBeVisible();
    // A link to another file opens it in Falcon.
    await doc.getByRole("link", { name: "guide" }).click();
    await expect(page).toHaveURL(/#docs\/guide\.md/);
    await expect(page.locator(".markdown-doc h1")).toHaveText("Guide");
    done();
  });

  test("images, SVG, PDF, audio, fonts, tables and binaries open in their viewers", async ({ page }) => {
    const done = watch(page);
    const id = await gammaID(page);
    await page.goto(`/r/${id}/#assets/pic.png`);
    await expect(page.locator(".media-meta").first()).toHaveText("64 × 40");
    await page.getByRole("button", { name: "Actual size" }).click();
    await expect(page.locator(".media-zoom")).toHaveText("100%");
    await page.getByRole("button", { name: "Zoom in" }).click();
    await expect(page.locator(".media-zoom")).toHaveText("125%");
    // SVG: shown as an image (its script never runs), with its source a click away.
    await page.goto(`/r/${id}/#assets/logo.svg`);
    await expect.poll(() => page.locator(".media-canvas img").evaluate((i: HTMLImageElement) => i.naturalWidth)).toBeGreaterThan(0);
    expect(await page.evaluate(() => (window as any).svgRan)).toBeUndefined();
    await page.getByRole("button", { name: "Source", exact: true }).click();
    await expect(page.locator(".code-text").first()).toContainText("<svg");
    // PDF: the browser's own viewer, in a frame from Falcon.
    await page.goto(`/r/${id}/#docs/report.pdf`);
    const frame = page.locator("iframe.media-pdf");
    await expect(frame).toBeVisible();
    const pdf = await page.request.get((await frame.getAttribute("src"))!);
    expect(pdf.headers()["content-type"]).toBe("application/pdf");
    expect((await pdf.body()).subarray(0, 5).toString()).toBe("%PDF-");
    // Audio.
    await page.goto(`/r/${id}/#assets/beep.wav`);
    await expect(page.locator(".media-audio audio")).toBeVisible();
    // Font specimen.
    await page.goto(`/r/${id}/#assets/mono.woff2`);
    await expect(page.locator(".font-specimen")).toContainText("quick brown fox");
    // Tables, with quoted commas kept in their cell.
    await page.goto(`/r/${id}/#data.csv`);
    await expect(page.locator(".media-meta").first()).toContainText("2 rows · 3 columns");
    await expect(page.locator(".media-table tbody tr").first()).toContainText("Falcon, viewer");
    // Anything else: what it is and a way to save it.
    await page.goto(`/r/${id}/#assets/blob.bin`);
    await expect(page.locator(".media-binary")).toContainText("1.0 KB");
    await expect(page.locator(".media-binary a[download]")).toBeVisible();
    // HTML is code, never rendered.
    await page.goto(`/r/${id}/#page.html`);
    await expect(page.locator(".code-text").first()).toContainText("<script>");
    done();
  });

  test("raw files are served so nothing in them can run", async ({ page }) => {
    const id = await gammaID(page);
    const svg = await page.request.get(`/api/r/${id}/raw?path=assets/logo.svg`);
    expect(svg.headers()["content-security-policy"]).toContain("sandbox");
    const html = await page.request.get(`/api/r/${id}/raw?path=page.html`);
    expect(html.headers()["content-type"]).toContain("text/plain");
    const range = await page.request.get(`/api/r/${id}/raw?path=docs/report.pdf`, { headers: { Range: "bytes=0-4" } });
    expect(range.status()).toBe(206);
    expect((await range.body()).toString()).toBe("%PDF-");
    expect((await page.request.get(`/api/r/${id}/raw?path=.git/config`)).ok()).toBeFalsy();
  });
});

test.describe("icons and themes", () => {
  test("file icons are tinted line icons, no image requests", async ({ page }) => {
    const done = watch(page);
    const icons: string[] = [];
    page.on("request", (r) => r.url().includes("file-icons") && icons.push(r.url()));
    const id = await gammaID(page);
    await page.goto(`/r/${id}/`);
    await expect(page.getByRole("treeitem", { name: "README.md" })).toBeVisible();
    const readme = page.getByRole("treeitem", { name: "README.md" }).locator(".file-type-icon");
    await expect(readme).toHaveAttribute("data-icon", "readme");
    await expect(page.getByRole("treeitem", { name: "data.csv" }).locator(".file-type-icon")).toHaveAttribute("data-icon", "table");
    await expect(page.getByRole("treeitem", { name: "assets" }).locator(".file-type-icon svg")).toBeVisible();
    expect(await page.locator(".file-sidebar img").count()).toBe(0);
    expect(icons).toEqual([]);
    done();
  });

  test("light and dark themes switch, cross-fade and are remembered", async ({ page }) => {
    const done = watch(page);
    const id = await gammaID(page);
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto(`/r/${id}/`);
    await page.evaluate(() => localStorage.removeItem("falcon.theme"));
    await page.reload();
    const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const dark = await bg();
    await page.getByRole("button", { name: /^Theme:/ }).click(); // system → light
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await expect.poll(bg).not.toBe(dark);
    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await page.getByRole("button", { name: /^Theme:/ }).click(); // light → dark
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.getByRole("button", { name: /^Theme:/ }).click(); // dark → system
    await expect(page.locator("html")).not.toHaveAttribute("data-theme", /.+/);
    // Following the system.
    await page.emulateMedia({ colorScheme: "light" });
    await expect.poll(bg).not.toBe(dark);
    done();
  });
});

test.describe("quiet when idle", () => {
  test("an open folder with nothing happening makes almost no requests", async ({ page }) => {
    const id = await gammaID(page);
    await page.goto(`/r/${id}/`);
    await expect(page.getByRole("treeitem", { name: "README.md" })).toBeVisible();
    await page.waitForTimeout(1500);
    let count = 0;
    page.on("request", (r) => new URL(r.url()).pathname.startsWith("/api/") && count++);
    await page.waitForTimeout(4000);
    expect(count, "requests in four idle seconds").toBeLessThanOrEqual(2);
  });
});

test.describe("mark", () => {
  test("the falcon dives in, rests, and beats its wings while Falcon works", async ({ page }) => {
    const done = watch(page);
    const id = await gammaID(page);
    // Hold one answer back so Falcon is visibly busy.
    let release = () => {};
    await page.route((url) => url.pathname.endsWith("/search"), async (route) => {
      await new Promise<void>((r) => (release = r));
      await route.continue();
    });
    await page.goto(`/r/${id}/`);
    const mark = page.locator(".activity-mark .falcon-mark");
    await expect(mark).toHaveClass(/intro/);
    await expect(mark).toHaveClass(/idle/, { timeout: 4000 });
    await page.keyboard.press(`${mod}+Shift+f`);
    await page.getByLabel("Search the folder").fill("needle");
    await expect(mark).toHaveClass(/working/, { timeout: 3000 });
    release();
    await expect(mark).toHaveClass(/idle/, { timeout: 3000 });
    // People who ask for less motion get a still mark.
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.reload();
    await expect(page.locator(".activity-mark .falcon-mark")).toHaveClass(/reduced/);
    done();
  });
});

test.describe("git client", () => {
  test("side-by-side diff marks changed characters; chosen lines are staged and unstaged", async ({ page }) => {
    // Whatever happens, list.txt goes back to what was committed (index and working tree).
    page.on("close", () => {
      try {
        git("reset -q -- list.txt");
        git("checkout -q -- list.txt");
      } catch {}
    });
    const done = watch(page);
    const id = await gammaID(page);
    writeFileSync(join(gamma, "list.txt"), readFileSync(join(gamma, "list.txt"), "utf8").replace("two\n", "two and more\n").replace("ten", "TEN"));
    await page.goto(`/r/${id}/`);
    await page.evaluate(() => localStorage.setItem("falcon.diff.split", "0"));
    await page.getByLabel("Source Control").click();
    await page.getByRole("treeitem", { name: "list.txt" }).locator("button").first().click();
    await expect(page.locator(".diff-hunk")).toHaveCount(2);
    // Side by side, with the changed words marked.
    await page.getByRole("button", { name: "Side by side" }).click();
    await expect(page.locator(".diff-pair").first()).toBeVisible();
    await expect.poll(() => page.evaluate(() => (CSS as any).highlights.get("diff-word-add")?.size || 0)).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Inline" }).click();
    // Choose the two lines of the first change (removed "two", added "two and more") and stage just those.
    const first = page.locator('.diff-row[data-h="0"]');
    await first.filter({ hasText: "two" }).locator(".diff-ln").first().click();
    await first.filter({ hasText: "two and more" }).locator(".diff-ln").first().click({ modifiers: ["Shift"] });
    await expect(page.locator(".diff-chosen")).toContainText("2 lines");
    await page.getByRole("button", { name: "Stage lines" }).click();
    await expect.poll(() => git("show :list.txt")).toContain("two and more");
    expect(git("show :list.txt")).toContain("ten");
    await expect(page.locator(".diff-hunk")).toHaveCount(1);
    // Whole file shows every line, then back to the changes.
    await page.getByRole("button", { name: "Whole file" }).click();
    await expect(page.locator(".diff-row")).toHaveCount(11);
    await page.getByRole("button", { name: "Changes only" }).click();
    // In the staged diff, unstage the line again.
    await page.getByRole("treeitem", { name: "list.txt" }).nth(0).locator("button").first().click();
    await expect(page.locator(".diff-hunk")).toHaveCount(1);
    await page.locator(".diff-row.add .diff-ln").first().click();
    await page.getByRole("button", { name: "Unstage lines" }).click();
    await expect.poll(() => git("show :list.txt")).not.toContain("two and more");
    git("checkout -- list.txt");
    done();
  });

  test("history is a graph across branches, with branch and tag labels", async ({ page }) => {
    const done = watch(page);
    const id = await gammaID(page);
    await page.goto(`/r/${id}/`);
    await page.evaluate(() => localStorage.removeItem("falcon.history.all"));
    await page.getByLabel("Source Control").click();
    await page.getByRole("tab", { name: "History" }).click();
    await expect(page.locator(".git-graph-row")).toHaveCount(3);
    await expect(page.locator(".git-ref.head")).toHaveText("main");
    await expect(page.locator(".git-ref.tag")).toHaveText("v1.0");
    await expect(page.locator(".git-ref", { hasText: "side" })).toBeVisible();
    // The merge commit has two lines leaving it.
    const merge = page.locator(".git-graph-row", { hasText: "Merge side" });
    await expect(merge.locator("circle.merge")).toHaveCount(1);
    // Opening a commit shows its files and diff.
    await page.locator(".git-graph-row", { hasText: "Side work" }).click();
    await expect(page.locator(".git-commit-detail")).toContainText("side.txt");
    await expect(page.locator(".diff-row.add")).toContainText("side");
    done();
  });
});
