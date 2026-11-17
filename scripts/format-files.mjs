import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync, lstatSync, existsSync, mkdtempSync, rmSync } from "node:fs";
import { dirname, resolve, extname, basename, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

export const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(join(root, "package.json"));
const policy = JSON.parse(readFileSync(new URL("./format-policy.json", import.meta.url), "utf8"));
const options = JSON.parse(
  readFileSync(
    policy.optionsPath
      ? resolve(root, policy.optionsPath)
      : new URL("./format-options.json", import.meta.url),
    "utf8",
  ),
);
const webExtensions = new Set(
  ".js .mjs .cjs .jsx .ts .mts .cts .tsx .json .jsonc .json5 .yaml .yml .toml .html .htm .css .scss .less .md .mdx .markdown .svelte .vue .graphql .gql".split(
    " ",
  ),
);
const binaryExtensions = new Set(
  ".png .jpg .jpeg .gif .webp .ico .icns .woff .woff2 .ttf .otf .pdf .zip .gz .br .mp3 .wav .mp4 .model .pem .aff .dic".split(
    " ",
  ),
);

function execute(command, args, input, { cwd = root, accepted = [0] } = {}) {
  const result = spawnSync(command, args, {
    cwd,
    input,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
    windowsHide: true,
  });
  if (result.error || !accepted.includes(result.status))
    throw new Error(
      `${command} ${args.join(" ")}: ${result.error?.message ?? result.stderr ?? result.stdout}`,
    );
  return result;
}

function git(args, input) {
  return execute("git", args, input).stdout;
}

export function formatterFor(path) {
  const name = basename(path);
  if (
    /^(?:node_modules|vendor|dist|build|target|\.git|\.venv)\//.test(path) ||
    /\/(?:node_modules|vendor|dist|build|target|\.venv)\//.test(path)
  )
    return "ignored";
  if (
    binaryExtensions.has(extname(path).toLowerCase()) ||
    /^(?:LICENSE|COPYING)(?:\.|$)/i.test(name) ||
    path.includes("/fonts/")
  )
    return "asset";
  if (
    /(?:^|\/)(?:package-lock\.json|pnpm-lock\.yaml|yarn\.lock|Cargo\.lock|uv\.lock|go\.sum)$/.test(
      path,
    )
  )
    return "lockfile";
  if (policy.immutable?.includes(path)) return "immutable migration";
  if (policy.jsxFixtures?.includes(path)) return "vite-plus";
  const ext = extname(path).toLowerCase();
  if (basename(path) === "go.mod") return "gomod";
  if (ext === ".go" || policy.goFixtures?.includes(path)) return "gofmt";
  if (ext === ".py" || ext === ".pyi") return "ruff";
  if (ext === ".rs") return "rustfmt";
  if (webExtensions.has(ext)) return "vite-plus";
  if ([".sh", ".bash", ".zsh"].includes(ext) || path.startsWith(".githooks/")) return "shell";
  if (name === "Dockerfile" || ext === ".dockerfile") return "dockerfile";
  if ([".svg", ".xml"].includes(ext)) return "xml";
  if ([".ps1", ".psm1", ".psd1"].includes(ext)) return "powershell";
  if (ext === ".prisma") return "prisma";
  if (ext === ".sql") return "sql";
  if (ext === ".conf" || path.endsWith(".conf.template")) return "nginx";
  // Text formats without a syntax formatter (Makefile, git attributes, env,
  // requirements, fixtures, etc.) still get LF, trailing-space and EOF fixes.
  return "text";
}

async function dependency(specifier) {
  return import(import.meta.resolve(specifier));
}

export async function formatContent(path, contents) {
  const formatter = formatterFor(path);
  if (["ignored", "asset", "lockfile", "immutable migration"].includes(formatter)) return contents;
  // Detect binary and non-UTF8 content before applying text normalization.
  const buffer = Buffer.isBuffer(contents) ? contents : Buffer.from(contents);
  if (buffer.includes(0) || !Buffer.from(buffer.toString("utf8")).equals(buffer)) {
    if (formatter === "text") return contents;
    throw new Error(`${path}: source must be UTF-8 text.`);
  }
  let source = buffer.toString("utf8").replace(/\r\n?/g, "\n");
  let formatted;
  if (formatter === "vite-plus") {
    const { format } = await dependency("vite-plus/fmt");
    const filename = policy.jsxFixtures?.includes(path) ? `${path}.jsx` : path;
    const result = await format(resolve(root, filename), source, options);
    if (result.errors.length)
      throw new Error(`${path}: ${result.errors.map((error) => error.message).join("\n")}`);
    formatted = result.code;
  } else if (formatter === "gofmt") {
    formatted = execute("gofmt", [], source).stdout;
  } else if (formatter === "gomod") {
    const directory = mkdtempSync(join(tmpdir(), "nox-gomod-format-"));
    try {
      const file = join(directory, "go.mod");
      writeFileSync(file, source);
      execute("go", ["mod", "edit", "-fmt"], undefined, { cwd: directory });
      formatted = readFileSync(file, "utf8");
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  } else if (formatter === "ruff") {
    formatted = execute(
      "uv",
      [
        "tool",
        "run",
        "--from",
        `ruff==${policy.ruffVersion}`,
        "ruff",
        "format",
        "--stdin-filename",
        path,
        "-",
      ],
      source,
    ).stdout;
  } else if (formatter === "rustfmt") {
    formatted = execute(
      "rustfmt",
      [
        "--edition",
        policy.rustEdition ?? "2021",
        "--emit",
        "stdout",
        "--config",
        "skip_children=true",
      ],
      source,
    ).stdout;
  } else if (formatter === "sql") {
    const { format } = await dependency("sql-formatter");
    formatted = format(source, { language: policy.sqlDialect ?? "postgresql", tabWidth: 2 });
  } else if (formatter === "prisma") {
    const directory = mkdtempSync(join(tmpdir(), "nox-prisma-format-"));
    try {
      const file = join(directory, "schema.prisma");
      writeFileSync(file, source);
      execute(process.execPath, [
        require.resolve("prisma/build/index.js"),
        "format",
        "--schema",
        file,
      ]);
      formatted = readFileSync(file, "utf8");
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  } else if (["shell", "dockerfile", "xml", "powershell", "nginx"].includes(formatter)) {
    const plugins = {
      shell: "prettier-plugin-sh",
      dockerfile: "prettier-plugin-sh",
      xml: "@prettier/plugin-xml",
      powershell: "prettier-plugin-powershell",
      nginx: "prettier-plugin-nginx",
    };
    const { format } = await dependency("prettier");
    const plugin = await dependency(plugins[formatter]);
    formatted = await format(source, {
      filepath: resolve(root, path),
      parser: formatter === "shell" ? "sh" : formatter,
      plugins: [plugin.default ?? plugin],
      tabWidth: 2,
      useTabs: false,
      endOfLine: "lf",
      xmlWhitespaceSensitivity: "strict",
    });
  } else {
    // Keep indentation and meaningful internal whitespace in unknown formats.
    formatted = source.replace(/[\t ]+$/gm, "");
  }
  return formatted ? formatted.replace(/\n*$/, "\n") : "";
}

function indexEntries(revision) {
  const args = revision
    ? ["ls-tree", "-rz", "--full-tree", revision]
    : ["ls-files", "--stage", "-z"];
  return git(args)
    .split("\0")
    .filter(Boolean)
    .map((entry) => {
      const [header, path] = entry.split(/\t(.*)/s);
      const fields = header.split(" ");
      return {
        mode: fields[0],
        oid: fields[revision ? 2 : 1],
        stage: revision ? "0" : fields[2],
        path,
      };
    });
}

export async function formatFiles({ staged = false, check = false, revision } = {}) {
  if (revision && (!check || staged))
    throw new Error("A revision can only be checked, never rewritten.");
  const selected = staged
    ? new Set(
        git(["diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z"])
          .split("\0")
          .filter(Boolean),
      )
    : null;
  const entries = indexEntries(revision).filter(
    ({ mode, stage, path }) =>
      stage === "0" &&
      (mode === "100644" || mode === "100755") &&
      (!selected || selected.has(path)),
  );
  if (!staged && !revision) {
    for (const path of git(["ls-files", "--others", "--exclude-standard", "-z"])
      .split("\0")
      .filter(Boolean))
      entries.push({ path });
  }
  const changes = [];
  for (const { path, mode, oid } of entries) {
    if (["ignored", "asset", "lockfile", "immutable migration"].includes(formatterFor(path)))
      continue;
    const file = resolve(root, path);
    if (!staged && !revision && (!existsSync(file) || !lstatSync(file).isFile())) continue;
    const blob =
      staged || revision
        ? spawnSync("git", ["cat-file", "blob", oid], { cwd: root, maxBuffer: 64 * 1024 * 1024 })
        : null;
    if (blob && blob.status !== 0) throw new Error(`Cannot read staged ${path}`);
    const original = blob ? blob.stdout : readFileSync(file);
    // cat-file's UTF8 decoding must not turn binary staged blobs into text.
    if (["ignored", "asset", "lockfile", "immutable migration"].includes(formatterFor(path)))
      continue;
    let formatted;
    try {
      formatted = Buffer.from(await formatContent(path, original));
    } catch (error) {
      throw new Error(`${path}: ${error.message}`);
    }
    if (original.equals(formatted)) continue;
    changes.push({ path, mode, original, formatted });
  }
  if (check && changes.length)
    throw new Error(
      `Files need formatting${revision ? ` in commit ${revision}; format and commit the corrections` : ""}. Run npm run format:\n${changes.map(({ path }) => path).join("\n")}`,
    );
  if (check) return [];
  // Compute every result before changing files or the index. For partially
  // staged files, reapply the unstaged edits using Git's three-way merge.
  for (const change of changes) {
    const file = resolve(root, change.path);
    if (staged) {
      if (!existsSync(file) || !lstatSync(file).isFile())
        throw new Error(
          `${change.path}: working file is missing or not regular; restore it before committing.`,
        );
      const working = readFileSync(file);
      if (working.equals(change.original)) change.working = change.formatted;
      else {
        const directory = mkdtempSync(join(tmpdir(), "nox-format-merge-"));
        try {
          const paths = [working, change.original, change.formatted].map((value, i) => {
            const temporary = join(directory, String(i));
            writeFileSync(temporary, value);
            return temporary;
          });
          const merged = execute("git", ["merge-file", "-p", ...paths], undefined, {
            accepted: [0, 1],
          });
          if (merged.status)
            throw new Error(
              `${change.path}: formatting overlaps unstaged edits. Stage those edits or stash them, then retry; no files were changed.`,
            );
          change.working = Buffer.from(merged.stdout);
        } finally {
          rmSync(directory, { recursive: true, force: true });
        }
      }
      change.oid = git(["hash-object", "-w", "--stdin"], change.formatted).trim();
    } else change.working = change.formatted;
  }
  if (staged && changes.length)
    git(
      ["update-index", "-z", "--index-info"],
      changes.map(({ mode, oid, path }) => `${mode} ${oid}\t${path}\0`).join(""),
    );
  for (const { path, working } of changes) writeFileSync(resolve(root, path), working);
  console.log(`${staged ? "Staged" : "Formatted"}: ${changes.length} file(s).`);
  return changes.map(({ path }) => path);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const position = process.argv.indexOf("--revision");
  if (position !== -1 && !process.argv[position + 1])
    throw new Error("--revision requires a commit.");
  formatFiles({
    staged: process.argv.includes("--staged"),
    check: process.argv.includes("--check"),
    revision: position === -1 ? undefined : process.argv[position + 1],
  }).catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
