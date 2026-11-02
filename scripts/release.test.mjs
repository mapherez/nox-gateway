import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { test } from "node:test";
import {
  normalizeVersion,
  release,
  runReleaseChecks,
  validateReleaseTag,
} from "./release.mjs";
const git = (root, ...args) =>
  execFileSync("git", args, {
    cwd: root,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
async function repository(t) {
  const temp = await mkdtemp(resolve(tmpdir(), "gateway-release-"));
  t.after(() => rm(temp, { recursive: true, force: true }));
  const root = resolve(temp, "repo"),
    origin = resolve(temp, "origin.git");
  await mkdir(root);
  const manifest =
    JSON.stringify(
      { name: "nox-gateway", version: "0.0.0", private: true },
      null,
      2,
    ) + "\n";
  await writeFile(resolve(root, "package.json"), manifest);
  await writeFile(
    resolve(root, "go.mod"),
    "module gateway-release-fixture\n\ngo 1.26.0\n",
  );
  git(root, "init", "--initial-branch=main");
  git(root, "config", "user.name", "Gateway release fixture");
  git(root, "config", "user.email", "fixture@example.invalid");
  git(root, "config", "commit.gpgsign", "false");
  git(root, "config", "tag.gpgsign", "false");
  git(root, "config", "core.autocrlf", "false");
  git(root, "config", "core.hooksPath", resolve(temp, "no-hooks"));
  git(root, "add", ".");
  git(root, "commit", "-m", "Fixture");
  git(root, "init", "--bare", origin);
  git(root, "remote", "add", "origin", origin);
  git(root, "push", "origin", "main");
  return { root, origin, manifest, head: git(root, "rev-parse", "HEAD") };
}
test("release tags reject malformed, mismatched and Docker-incompatible versions", () => {
  for (const version of ["", "1.2", "01.0.0", "1.0.0\n", "1.0.0+build"])
    assert.throws(() => normalizeVersion(version));
  assert.equal(normalizeVersion("v1.2.3-rc.1"), "1.2.3-rc.1");
  assert.throws(() =>
    validateReleaseTag("v1.2.4", JSON.stringify({ version: "1.2.3" })),
  );
});
test("failed verification restores the version and does not create or push a release commit", async (t) => {
  const f = await repository(t);
  await assert.rejects(
    release(f.root, "1.0.0", {
      check: async () => {
        throw Error("verification failed");
      },
    }),
    /verification failed/,
  );
  assert.equal(
    await readFile(resolve(f.root, "package.json"), "utf8"),
    f.manifest,
  );
  assert.equal(git(f.root, "rev-parse", "HEAD"), f.head);
  assert.equal(git(f.root, "status", "--porcelain"), "");
  assert.equal(git(f.root, "tag", "--list"), "");
  assert.equal(git(f.origin, "rev-parse", "refs/heads/main"), f.head);
});
test("dirty trees and existing tags are refused before running checks", async (t) => {
  const f = await repository(t);
  await writeFile(resolve(f.root, "untracked"), "retain");
  await assert.rejects(
    release(f.root, "1.0.0", {
      check() {
        assert.fail("dirty release reached checks");
      },
    }),
    /alteracoes/,
  );
  await rm(resolve(f.root, "untracked"));
  git(f.root, "tag", "v1.0.0");
  await assert.rejects(
    release(f.root, "1.0.0", {
      check() {
        assert.fail("existing tag reached checks");
      },
    }),
    /existe localmente/,
  );
});
test("validated release commits only the package version and pushes commit/tag atomically to disposable origin", async (t) => {
  const f = await repository(t);
  let checked = false;
  await release(f.root, "1.0.0", {
    check: async (root, tag) => {
      assert.equal(root, f.root);
      assert.equal(tag, "v1.0.0");
      validateReleaseTag(
        tag,
        await readFile(resolve(root, "package.json"), "utf8"),
      );
      checked = true;
    },
  });
  assert.ok(checked);
  const head = git(f.root, "rev-parse", "HEAD");
  assert.notEqual(head, f.head);
  assert.equal(git(f.origin, "rev-parse", "refs/heads/main"), head);
  assert.equal(git(f.origin, "rev-parse", "v1.0.0^{}"), head);
  assert.equal(
    git(f.root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"),
    "package.json",
  );
});
test("verification runs at the actual Gateway module root and builds one Gateway image", async (t) => {
  const f = await repository(t);
  const calls = [];
  await runReleaseChecks(f.root, "v1.0.0", async (command, args, options) => {
    assert.equal(options.cwd, f.root);
    await readFile(resolve(options.cwd, "go.mod"));
    calls.push({ command, args });
  });
  assert.equal(calls.filter((c) => c.command === "docker").length, 1);
  assert.equal(calls.find((c) => c.command === "docker").args.at(-1), ".");
  assert.equal(
    calls.find((c) => c.command === "python3").args[0],
    "scripts/check-image.py",
  );
  assert.equal(
    calls.some((c) => c.args.includes("frontend")),
    false,
  );
});
