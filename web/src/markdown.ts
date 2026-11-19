// A small, safe Markdown renderer (GitHub-flavoured: tables, task lists, fences, strikethrough, autolinks)
// for README files and language-server hovers. All text is escaped; only a fixed set of tags is produced.
// Web links open in a new tab; links and images inside the repository are resolved by the host (the
// document viewer opens files in Falcon and shows images from the folder). Images on the web are not
// loaded: Falcon makes no network requests, so their alternative text is shown instead.

export interface RenderOptions {
  /** A repository-relative target for a local link or image (null if it leaves the folder). */
  resolve?: (href: string) => string | null;
  /** The URL that shows a repository file's bytes (for images). */
  raw?: (path: string) => string;
}

const escapeMap: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};
export const escape = (s: string) => s.replace(/[&<>"']/g, (c) => escapeMap[c]);

function slug(text: string) {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .trim()
    .replace(/\s/g, "-");
}



function inline(text: string, o: RenderOptions): string {
  // Protect code spans first; everything else is escaped before formatting.
  const codes: string[] = [];
  text = text.replace(/`([^`]+)`/g, (_, code: string) => {
    codes.push(code);
    return `\u0000${codes.length - 1}\u0000`;
  });
  const links: string[] = [];
  const keep = (html: string) => {
    links.push(html);
    return `\u0001${links.length - 1}\u0001`;
  };
  // Images first (a linked badge is an image inside a link).
  text = text.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, alt: string, src: string) => keep(image(alt, src, o)));
  text = text.replace(/\[((?:[^\]]|\u0001\d+\u0001)*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, label: string, href: string) => keep(link(label, href, o)));
  // The inline HTML READMEs use: a few formatting tags are kept, images and links resolved, the rest dropped.
  text = text.replace(/<!--[\s\S]*?-->/g, "");
  text = text.replace(/<img\b([^>]*)>/gi, (_, attrs: string) => {
    const html = image(attr(attrs, "alt"), attr(attrs, "src"), o);
    // Sizes written in the README are kept (numbers only).
    const size = ["width", "height"].map((a) => (/^\d{1,4}%?$/.test(attr(attrs, a)) ? ` ${a}="${attr(attrs, a)}"` : "")).join("");
    return keep(size ? html.replace("<img ", "<img" + size + " ") : html);
  });
  text = text.replace(/<a\b([^>]*)>([\s\S]*?)<\/a>/gi, (_, attrs: string, inner: string) => keep(link(inner, attr(attrs, "href"), o)));
  text = text.replace(/<(\/?)(br|kbd|sub|sup|b|i|em|strong|code|s|u|mark)\s*\/?>/gi, (_, close: string, tag: string) => keep(`<${close}${tag.toLowerCase()}>`));
  text = text.replace(/<\/?[a-z][\w-]*(?:\s[^>]*)?\/?>/gi, "");
  let out = escape(text);
  out = out
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/__([^_]+)__/g, "<strong>$1</strong>")
    .replace(/(^|[^*\w])\*([^*\s][^*]*?)\*(?!\w)/g, "$1<em>$2</em>")
    .replace(/(^|[^_\w])_([^_\s][^_]*?)_(?!\w)/g, "$1<em>$2</em>")
    .replace(/~~([^~]+)~~/g, "<del>$1</del>")
    .replace(
      /(^|[\s(])(https?:\/\/[^\s<)]+)/g,
      '$1<a href="$2" target="_blank" rel="noreferrer">$2</a>',
    );
  // Placeholders can nest (an image inside a link): fill them in until none remain.
  for (let n = 0; n < 4 && /\u0001\d+\u0001/.test(out); n++) out = out.replace(/\u0001(\d+)\u0001/g, (_, i) => links[Number(i)]);
  out = out.replace(/\u0000(\d+)\u0000/g, (whole, i) => {
    const code = codes[Number(i)];
    // A link's label is rendered on its own: its code spans belong to the enclosing text, which fills
    // them in afterwards (gopls says "[`lib.Greet` on pkg.go.dev](…)").
    if (code === undefined) return whole;
    return `<code>${escape(code)}</code>`;
  });
  return out;
}

function attr(attrs: string, name: string) {
  const m = attrs.match(new RegExp(`\\b${name}\\s*=\\s*("([^"]*)"|'([^']*)'|([^\\s>]+))`, "i"));
  return m ? (m[2] ?? m[3] ?? m[4] ?? "") : "";
}

function link(label: string, href: string, o: RenderOptions) {
  const text = /\u0001/.test(label) ? label : inline(label || href, o);
  if (/^(https?:|mailto:)/i.test(href)) {
    return `<a href="${escape(href)}" target="_blank" rel="noreferrer">${text}</a>`;
  }
  if (href.startsWith("#")) return `<a href="${escape(href)}" data-anchor="${escape(slug(decodeURIComponent(href.slice(1))))}">${text}</a>`;
  const target = o.resolve?.(href);
  if (target) {
    const [path, hash = ""] = target.split("#");
    const line = hash.match(/^L(\d+)/)?.[1] || "";
    return `<a href="#" class="md-file" data-path="${escape(path)}" data-line="${line}">${text}</a>`;
  }
  return text;
}

function image(alt: string, src: string, o: RenderOptions) {
  if (/^data:image\/(png|jpe?g|gif|webp);/i.test(src)) return `<img src="${escape(src)}" alt="${escape(alt)}">`;
  if (/^(https?:)?\/\//i.test(src)) {
    // Not loaded: no network.
    return alt ? `<span class="md-img-off" title="${escape(src)}">${escape(alt)}</span>` : "";
  }
  const target = o.resolve?.(src);
  if (target && o.raw) return `<img src="${escape(o.raw(target.split("#")[0]))}" alt="${escape(alt)}" loading="lazy">`;
  return alt ? `<span class="md-img-off">${escape(alt)}</span>` : "";
}

function table(lines: string[], o: RenderOptions) {
  const cells = (row: string) =>
    row
      .trim()
      .replace(/^\||\|$/g, "")
      .split(/(?<!\\)\|(?=(?:[^`]*`[^`]*`)*[^`]*$)/)
      .map((c) => c.trim().replace(/\\\|/g, "|"));
  const head = cells(lines[0]);
  const align = cells(lines[1]).map((c) =>
    c.endsWith(":") ? (c.startsWith(":") ? "center" : "right") : "",
  );
  const th = head
    .map((c, i) => `<th${align[i] ? ` style="text-align:${align[i]}"` : ""}>${inline(c, o)}</th>`)
    .join("");
  const body = lines
    .slice(2)
    .map(
      (row) =>
        "<tr>" +
        cells(row)
          .map((c, i) => `<td${align[i] ? ` style="text-align:${align[i]}"` : ""}>${inline(c, o)}</td>`)
          .join("") +
        "</tr>",
    )
    .join("");
  return `<div class="md-table"><table><thead><tr>${th}</tr></thead><tbody>${body}</tbody></table></div>`;
}

export function renderMarkdown(source: string, o: RenderOptions = {}): string {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const out: string[] = [];
  const used = new Map<string, number>();
  let i = 0;
  const isBlockStart = (l: string) =>
    /^(#{1,6}\s|```|~~~|>|\s*([-*+]|\d+[.)])\s|\s*(---|\*\*\*|___)\s*$)/.test(l) ||
    (l.includes("|") && /^\s*\|?[\s:-]+\|[\s|:-]*$/.test(lines[i + 1] || ""));
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = line.match(/^(\s*)(```|~~~)\s*([\w+-]*)/);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence[2])) body.push(lines[i++]);
      i++;
      const lang = fence[3] ? ` data-lang="${escape(fence[3].toLowerCase())}"` : "";
      out.push(`<pre class="md-code"${lang}><code>${escape(body.join("\n"))}</code></pre>`);
      continue;
    }
    const heading = line.match(/^(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (heading) {
      const level = heading[1].length;
      const text = heading[2];
      let id = slug(text.replace(/[`*_]/g, ""));
      const n = used.get(id) || 0;
      used.set(id, n + 1);
      if (n) id += "-" + n;
      out.push(`<h${level} id="${escape(id)}">${inline(text, o)}</h${level}>`);
      i++;
      continue;
    }
    if (/^\s*(---|\*\*\*|___)\s*$/.test(line)) {
      out.push("<hr>");
      i++;
      continue;
    }
    if (line.startsWith(">")) {
      const body: string[] = [];
      while (i < lines.length && lines[i].startsWith(">")) body.push(lines[i++].replace(/^>\s?/, ""));
      out.push(`<blockquote>${renderMarkdown(body.join("\n"), o)}</blockquote>`);
      continue;
    }
    if (line.includes("|") && /^\s*\|?[\s:-]+\|[\s|:-]*$/.test(lines[i + 1] || "")) {
      const rows: string[] = [];
      while (i < lines.length && lines[i].includes("|") && lines[i].trim()) rows.push(lines[i++]);
      out.push(table(rows, o));
      continue;
    }
    const item = line.match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
    if (item) {
      const ordered = /\d/.test(item[2]);
      const indent = item[1].length;
      const items: string[] = [];
      while (i < lines.length) {
        const m = lines[i].match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
        if (!m || m[1].length < indent) break;
        if (m[1].length > indent) {
          // Nested list: collect deeper lines and render recursively.
          const nested: string[] = [];
          while (i < lines.length) {
            const n = lines[i].match(/^(\s*)/)![1].length;
            if (!lines[i].trim() || n <= indent) break;
            nested.push(lines[i++].slice(indent + 2));
          }
          items[items.length - 1] += renderMarkdown(nested.join("\n"), o);
          continue;
        }
        let text = m[3];
        i++;
        while (i < lines.length && lines[i].trim() && /^\s{2,}\S/.test(lines[i]) && !/^\s*([-*+]|\d+[.)])\s/.test(lines[i])) {
          text += " " + lines[i++].trim();
        }
        const task = text.match(/^\[([ xX])\]\s+(.*)$/);
        items.push(
          task
            ? `<span class="md-check${task[1] !== " " ? " done" : ""}"></span>${inline(task[2], o)}`
            : inline(text, o),
        );
      }
      const tag = ordered ? "ol" : "ul";
      out.push(`<${tag}>${items.map((x) => `<li>${x}</li>`).join("")}</${tag}>`);
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && (para.length === 0 || !isBlockStart(lines[i]))) {
      para.push(lines[i++].trim());
    }
    const html = inline(para.join(" "), o);
    if (html.trim()) out.push(`<p>${html}</p>`);
  }
  return out.join("\n");
}

