// The language features beyond navigation, in the browser, against a real gopls: occurrences of the name
// at the caret, rename with a preview, the call hierarchy, quick fixes, the folder's problems and symbols
// from the language server. A second Falcon (language servers on) serves a small Go module. Skipped when
// gopls is not installed.
import { test, expect, type Page } from "@playwright/test";
import { execSync, spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const project = join(import.meta.dirname, "..", "..");
const dir = join(import.meta.dirname, "..", ".e2e-lsp");
const base = "http://127.0.0.1:7735";
const gopls = [join(homedir(), "go", "bin", "gopls"), "/usr/local/bin/gopls", "/usr/bin/gopls"].find(existsSync) || (() => {
  try {
    return execSync("command -v gopls", { encoding: "utf8" }).trim();
  } catch {
    return "";
  }
})();
let server: ChildProcess | undefined;

test.describe.configure({ mode: "serial" });
test.skip(!gopls, "gopls is not installed");

const files: Record<string, string> = {
  "go.mod": "module example.test/shop\n\ngo 1.22\n",
  "text/text.go": "package text\n\n// Upper is used twice.\nfunc Upper(s string) string { return s }\n",
  "api/api.go":
    'package api\n\nimport (\n\t"strings"\n\n\t"example.test/shop/text"\n)\n\nfunc Handle() string { return text.Upper("x") + text.Upper("y") }\n',
};

test.beforeAll(async () => {
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(join(dir, "shop"), { recursive: true });
  for (const [p, c] of Object.entries(files)) {
    mkdirSync(join(dir, "shop", p, ".."), { recursive: true });
    writeFileSync(join(dir, "shop", p), c);
  }
  const git = (args: string) => execSync(`git -C ${join(dir, "shop")} ${args}`);
  git("init -q -b main");
  git("-c user.name=t -c user.email=t@example.test add -A");
  git("-c user.name=t -c user.email=t@example.test -c commit.gpgSign=false commit -qm initial");
  execSync(`go build -o ${join(dir, "falcon")} ./cmd/falcon`, { cwd: project });
  server = spawn(join(dir, "falcon"), ["-data", join(dir, "data"), "-listen", "127.0.0.1:7735", "-assets", join(project, "web", "dist"), join(dir, "shop")], {
    stdio: "ignore",
    env: { ...process.env, PATH: `${join(homedir(), "go", "bin")}:${process.env.PATH}` },
  });
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(base + "/healthz")).ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("the language-server Falcon did not start");
});
test.afterAll(() => {
  server?.kill();
});

async function open(page: Page, path: string) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const { repos } = await (await page.request.get(base + "/api/repos")).json();
  await page.goto(`${base}/r/${repos[0].id}/#${path}`);
  await expect(page.locator('.code-row[data-line="1"] .code-text')).toContainText("package");
  return () => expect(errors, "page errors").toEqual([]);
}
const row = (page: Page, n: number) => page.locator(`.code-row[data-line="${n}"] .code-text`);
// Puts the caret on a column of a line (clicks at that character).
async function caret(page: Page, line: number, col: number) {
  const box = await page.evaluate(
    ({ line, col }) => {
      const code = document.querySelector(`.code-row[data-line="${line}"] .code-text`)!;
      const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
      let at = 0;
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const len = n.nodeValue!.length;
        if (col < at + len) {
          const r = document.createRange();
          r.setStart(n, col - at);
          r.setEnd(n, col - at + 1);
          const b = r.getBoundingClientRect();
          return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
        }
        at += len;
      }
      return null;
    },
    { line, col },
  );
  await page.mouse.click(box!.x, box!.y);
}
const highlighted = (page: Page) =>
  page.evaluate(() => ["occ-text", "occ-read", "occ-write"].reduce((n, k) => n + ((CSS as any).highlights.get(k)?.size || 0), 0));

test("occurrences, problems, quick fix, call hierarchy, symbols and rename", async ({ page }) => {
  test.setTimeout(150000); // gopls loads the module first
  const done = await open(page, "api/api.go");
  // Problems: gopls reports the unused import; the status bar counts it, the Problems view lists it.
  await expect(page.locator('.statusbar button[aria-label="Problems"]')).toContainText("1", { timeout: 30000 });
  await page.keyboard.press("Control+Shift+M");
  await expect(page.locator(".problems-list")).toContainText('"strings" imported and not used');

  // Occurrences: the caret on Upper marks both uses.
  await caret(page, 9, 36);
  await expect.poll(() => highlighted(page), { timeout: 20000 }).toBeGreaterThanOrEqual(2);

  // Call hierarchy from the declaration: Handle calls Upper (twice).
  await page.goto(page.url().replace(/#.*$/, "#text/text.go"));
  await expect(row(page, 4)).toContainText("func Upper");
  await caret(page, 4, 6);
  await page.keyboard.press("Shift+Alt+H");
  await expect(page.locator(".calls")).toContainText("Callers of Upper");
  await expect(page.locator(".call-row").nth(1)).toContainText("Handle", { timeout: 20000 });
  await expect(page.locator(".call-row").nth(1)).toContainText("2 calls");
  await page.locator(".calls button", { hasText: "Back to source" }).click();

  // Symbols across the folder, from gopls as well as Falcon's index.
  await page.keyboard.press("Control+Shift+O");
  await page.keyboard.type("Handle");
  await expect(page.locator(".palette")).toContainText("api/api.go");
  await page.keyboard.press("Escape");

  // Rename Upper to Shout: a preview of both files, then applied.
  await caret(page, 4, 6);
  await page.keyboard.press("F2");
  const box = page.locator(".rename-box");
  await expect(box).toHaveValue("Upper");
  await box.fill("Shout");
  await box.press("Enter");
  const preview = page.locator(".change-preview");
  await expect(preview).toContainText("Rename Upper to Shout");
  await expect(preview).toContainText("3 changes in 2 files");
  await preview.locator("button", { hasText: "Apply" }).click();
  await expect.poll(() => readFileSync(join(dir, "shop", "api", "api.go"), "utf8")).toContain('text.Shout("x") + text.Shout("y")');
  await expect(row(page, 4)).toContainText("func Shout", { timeout: 10000 });

  // A quick fix on the unused import removes it.
  await page.goto(page.url().replace(/#.*$/, "#api/api.go"));
  await expect(row(page, 4)).toContainText('"strings"');
  await caret(page, 4, 3);
  await page.keyboard.press("Control+.");
  const fixes = page.locator(".code-actions");
  await expect(fixes).toBeVisible({ timeout: 20000 });
  await fixes.locator("button", { hasText: /import/i }).first().click();
  await expect.poll(() => readFileSync(join(dir, "shop", "api", "api.go"), "utf8")).not.toContain('"strings"');
  // And its problem goes away.
  await expect(page.locator('.statusbar button[aria-label="Problems"]')).toHaveText(/^\s*0\s*0\s*$/, { timeout: 30000 });
  done();
});
