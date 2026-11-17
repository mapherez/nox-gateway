import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  copyFileSync,
  symlinkSync,
  rmSync,
  chmodSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { formatContent, root } from "./format-files.mjs";

const scripts = fileURLToPath(new URL(".", import.meta.url));
const policy = JSON.parse(readFileSync(new URL("./format-policy.json", import.meta.url), "utf8"));
function command(cwd, executable, args, expected = 0) {
  const result = spawnSync(executable, args, {
    cwd,
    encoding: "utf8",
    env: { ...process.env, NOX_SKIP_HOOKS: "0" },
  });
  assert.equal(
    result.status,
    expected,
    `${executable} ${args.join(" ")}\n${result.stdout}\n${result.stderr}`,
  );
  return result.stdout;
}
function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), "nox-hook-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(join(directory, "scripts"));
  mkdirSync(join(directory, ".githooks"));
  for (const name of ["format-files.mjs", "format-policy.json", "git-hooks.mjs"])
    copyFileSync(join(scripts, name), join(directory, "scripts", name));
  copyFileSync(
    policy.optionsPath ? join(root, policy.optionsPath) : join(scripts, "format-options.json"),
    join(directory, "scripts/format-options.json"),
  );
  const fixturePolicy = { ...policy };
  delete fixturePolicy.optionsPath;
  writeFileSync(join(directory, "scripts/format-policy.json"), JSON.stringify(fixturePolicy));
  writeFileSync(join(directory, "scripts/dev-checks.json"), '{"check":[],"vite":[]}');
  writeFileSync(join(directory, "package.json"), "{}");
  symlinkSync(join(root, "node_modules"), join(directory, "node_modules"), "junction");
  writeFileSync(join(directory, ".gitignore"), "node_modules/\nscripts/\npackage.json\n");
  writeFileSync(
    join(directory, ".githooks/pre-commit"),
    "#!/bin/sh\nexec node scripts/git-hooks.mjs commit\n",
  );
  chmodSync(join(directory, ".githooks/pre-commit"), 0o755);
  command(directory, "git", ["init", "-q"]);
  command(directory, "git", ["config", "core.hooksPath", ".githooks"]);
  command(directory, "git", ["config", "user.name", "Hook test"]);
  command(directory, "git", ["config", "user.email", "hook@example.invalid"]);
  command(directory, "git", ["config", "commit.gpgsign", "false"]);
  return directory;
}

void test("all web/text formatters are idempotent and insert a final newline", async () => {
  for (const [path, source] of [
    ["a.ts", "export const a={ x:1 }"],
    ["a.js", "const a={x:1}"],
    ["a.css", ".a{margin:0  2px;color:red}"],
    ["a.scss", "$gap:2px;.a{margin:$gap;&:hover{color:red}}"],
    ["a.json", '{"a":1}'],
    ["a.yaml", "a:   1"],
    ["a.toml", "a=1"],
    ["a.md", "# Title\n\n* a\n* b"],
    ["a.html", "<div><span>hi</span></div>"],
    ["a.sh", "#!/bin/sh\nif true;then echo hi;fi"],
    ["Dockerfile", "FROM alpine\nRUN echo    hi"],
    ["a.svg", '<svg><path d="M0 0"/></svg>'],
    ["Makefile", "all:\n\techo hi  "],
  ]) {
    const formatted = await formatContent(path, source);
    assert.ok(formatted.endsWith("\n"), path);
    assert.equal(await formatContent(path, formatted), formatted, path);
  }
});

void test("native formatters format source and remain idempotent", async () => {
  const examples = [
    [".go", "a.go", "package example\nfunc f(){println(1)}"],
    [
      ".go",
      "go.mod",
      "module example.com/test\n\ngo 1.26.0\n\nrequire (\n example.com/dependency v1.0.0\n)",
    ],
    [".py", "a.py", "def f( ):\n return {'a':1}"],
    [".rs", "a.rs", 'pub fn f(){println!("hi");}'],
    [".ps1", "a.ps1", "if($true){Write-Output 'hi'}"],
    [
      ".prisma",
      "a.prisma",
      'datasource db {\nprovider = "sqlite"\nurl = "file:./test.db"\n}\nmodel Example {\nid Int @id\n}',
    ],
    [".sql", "new.sql", "select a,b from example where a=1;"],
    [".svelte", "a.svelte", "<script>let x=1</script><p>{x}</p>"],
  ];
  for (const [language, path, source] of examples) {
    if (!policy.languages.includes(language)) continue;
    const formatted = await formatContent(path, source);
    assert.notEqual(formatted, source, path);
    assert.equal(await formatContent(path, formatted), formatted, path);
  }
});

void test("a real git commit automatically includes formatting and preserves executable mode", (t) => {
  const directory = fixture(t);
  writeFileSync(join(directory, "with spaces.css"), ".a{margin:0  2px}");
  writeFileSync(join(directory, "breaks.md"), "first  \nsecond");
  writeFileSync(join(directory, "example.sh"), "#!/bin/sh\nif true;then echo hi;fi");
  chmodSync(join(directory, "example.sh"), 0o755);
  if (policy.languages.includes(".go"))
    writeFileSync(join(directory, "example.go"), "package example\nfunc f(){println(1)}");
  command(directory, "git", ["add", "."]);
  command(directory, "git", ["update-index", "--chmod=+x", "example.sh"]);
  command(directory, "git", ["commit", "-qm", "Exercise the real pre-commit hook"]);
  const css = command(directory, "git", ["show", "HEAD:with spaces.css"]);
  assert.ok(css.includes("margin: 0 2px;"));
  assert.ok(css.endsWith("\n"));
  assert.equal(command(directory, "git", ["show", "HEAD:breaks.md"]), "first  \nsecond\n");
  assert.match(command(directory, "git", ["ls-files", "--stage", "example.sh"]), /^100755 /);
  assert.equal(command(directory, "git", ["diff", "--name-only"]), "");
  command(directory, process.execPath, ["scripts/format-files.mjs", "--check"]);
});

void test("partially staged content stays partial after automatic formatting", (t) => {
  const directory = fixture(t);
  const path = join(directory, "example.ts");
  const staged =
    "export const first={a:1};\n\n// a\n// b\n// c\n// d\n// e\nexport const last = 1;\n";
  writeFileSync(path, staged);
  command(directory, "git", ["add", "example.ts"]);
  writeFileSync(path, staged.replace("last = 1", "last = 99"));
  command(directory, process.execPath, ["scripts/git-hooks.mjs", "commit"]);
  const index = command(directory, "git", ["show", ":example.ts"]);
  assert.ok(index.includes("first = { a: 1 }"));
  assert.ok(index.includes("last = 1"));
  assert.ok(readFileSync(path, "utf8").includes("last = 99"));
});

void test("overlapping unstaged edits abort without changing the index or worktree", (t) => {
  const directory = fixture(t);
  const path = join(directory, "example.ts");
  writeFileSync(path, "export const a={x:1}");
  command(directory, "git", ["add", "example.ts"]);
  writeFileSync(path, "export const a={x:999}");
  const before = command(directory, "git", ["write-tree"]);
  const contents = readFileSync(path);
  command(directory, process.execPath, ["scripts/git-hooks.mjs", "commit"], 1);
  assert.equal(command(directory, "git", ["write-tree"]), before);
  assert.deepEqual(readFileSync(path), contents);
});

void test("syntax failures leave every staged and working file unchanged", (t) => {
  const directory = fixture(t);
  writeFileSync(join(directory, "a.css"), ".a{color:red}");
  writeFileSync(join(directory, "z.ts"), "export const =");
  command(directory, "git", ["add", "."]);
  const before = command(directory, "git", ["write-tree"]);
  command(directory, process.execPath, ["scripts/git-hooks.mjs", "commit"], 1);
  assert.equal(command(directory, "git", ["write-tree"]), before);
  assert.equal(readFileSync(join(directory, "a.css"), "utf8"), ".a{color:red}");
});

void test("binary blobs, symlinks and generated lockfiles stay byte-for-byte unchanged", (t) => {
  const directory = fixture(t);
  const binary = Buffer.from([255, 254, 0, 100, 32]);
  writeFileSync(join(directory, "binary.dat"), binary);
  writeFileSync(join(directory, "package-lock.json"), '{"lockfileVersion":3}');
  if (process.platform !== "win32") symlinkSync("binary.dat", join(directory, "link"));
  command(directory, "git", ["add", "."]);
  command(directory, process.execPath, ["scripts/git-hooks.mjs", "commit"]);
  assert.deepEqual(readFileSync(join(directory, "binary.dat")), binary);
  assert.equal(readFileSync(join(directory, "package-lock.json"), "utf8"), '{"lockfileVersion":3}');
  if (process.platform !== "win32")
    assert.match(command(directory, "git", ["ls-files", "--stage", "link"]), /^120000 /);
});

void test("invalid UTF-8 source reports an error instead of silently skipping formatting", async () => {
  await assert.rejects(formatContent("invalid.ts", Buffer.from([0x96])), /UTF-8/);
});

void test("push checks the committed bytes even when the working file has already been fixed", (t) => {
  const directory = fixture(t);
  writeFileSync(join(directory, "example.css"), ".a{color:red}");
  command(directory, "git", ["add", "."]);
  command(directory, "git", [
    "-c",
    "core.hooksPath=",
    "commit",
    "-qm",
    "An older unformatted commit",
  ]);
  const fixed = ".a {\n  color: red;\n}\n";
  writeFileSync(join(directory, "example.css"), fixed);
  command(directory, process.execPath, ["scripts/format-files.mjs", "--check"]);
  command(
    directory,
    process.execPath,
    ["scripts/format-files.mjs", "--check", "--revision", "HEAD"],
    1,
  );
  assert.equal(readFileSync(join(directory, "example.css"), "utf8"), fixed);
});
