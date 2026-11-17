import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";

export const config = JSON.parse(
  readFileSync(new URL("./ci-config.json", import.meta.url), "utf8"),
);
const semver =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*)?$/;

export function versionTag(input) {
  const version = typeof input === "string" ? input.replace(/^v/, "") : "";
  if (!semver.test(version) || /\s/.test(version) || `v${version}`.length > 128) {
    throw new Error("Invalid version. Use X.Y.Z or X.Y.Z-prerelease, without build metadata.");
  }
  return `v${version}`;
}

export function git(root, ...args) {
  return execFileSync("git", args, {
    cwd: root,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

// Only explicitly listed version fields can be normalized. Dependency versions,
// scripts, file modes, and all other source/build inputs remain part of the gate.
export function renderVersion(contents, spec, version) {
  versionTag(version);
  if (spec.fields) {
    const value = JSON.parse(contents);
    for (const fields of spec.fields) {
      let parent = value;
      for (const key of fields.slice(0, -1)) parent = parent?.[key];
      const key = fields.at(-1);
      if (typeof parent?.[key] !== "string")
        throw new Error(`Missing version field in ${spec.path}.`);
      versionTag(parent[key]);
      parent[key] = version;
    }
    const newline = contents.includes("\r\n") ? "\r\n" : "\n";
    const indent = contents.match(/\n([\t ]+)"/)?.[1] ?? 2;
    return (
      JSON.stringify(value, null, indent).replaceAll("\n", newline) +
      (contents.endsWith("\n") ? newline : "")
    );
  }
  const blocks = contents.split(/(?=^\[)/m);
  const indexes = blocks.flatMap((block, index) => {
    const heading = block.split(/\r?\n/, 1)[0];
    const section = spec.section.startsWith("[") ? spec.section : `[${spec.section}]`;
    return heading === section &&
      (!spec.name || block.match(/^name\s*=\s*"([^"]+)"[ \t]*\r?$/m)?.[1] === spec.name)
      ? [index]
      : [];
  });
  if (indexes.length !== 1) throw new Error(`Expected one version section in ${spec.path}.`);
  const index = indexes[0];
  const pattern = /^(version[ \t]*=[ \t]*")([^"]+)(")/gm;
  const fields = [...blocks[index].matchAll(pattern)];
  if (fields.length !== 1) throw new Error(`Expected one version in ${spec.path}.`);
  versionTag(fields[0][2]);
  blocks[index] = blocks[index].replace(
    pattern,
    (_, prefix, _old, suffix) => prefix + version + suffix,
  );
  return blocks.join("");
}

export function releaseRequest(root, revision, { requireTag = false, expectedTag = "" } = {}) {
  if (!/^[a-f0-9]{40}$/.test(revision)) throw new Error("A full commit SHA is required.");
  const message = git(root, "show", "-s", "--format=%B", revision);
  const markers = message.split("\n").filter((line) => line.startsWith(config.marker));
  if (!markers.length) return null;
  if (markers.length !== 1) throw new Error("Ambiguous release request.");
  const input = markers[0].slice(config.marker.length + 1);
  const tag = versionTag(input);
  if (
    markers[0] !== `${config.marker} ${config.markerVersionOnly ? tag.slice(1) : tag}` ||
    message.split("\n")[0] !== `chore: release ${tag}` ||
    (expectedTag && expectedTag !== tag)
  ) {
    throw new Error("Invalid release marker or subject.");
  }
  const parents = git(root, "show", "-s", "--format=%P", revision).split(" ").filter(Boolean);
  if (parents.length !== 1) throw new Error("A release needs exactly one source parent.");
  const sourceSha = parents[0];
  const records = git(root, "diff", "--raw", "--no-abbrev", "--no-renames", sourceSha, revision)
    .split("\n")
    .filter(Boolean);
  if (!records.length && !config.allowEmpty) throw new Error("A release must change the version.");
  for (const record of records) {
    const [metadata, path] = record.split("\t");
    const [beforeMode, afterMode, , , status] = metadata.slice(1).split(" ");
    if (
      beforeMode !== afterMode ||
      status !== "M" ||
      !config.files.some((spec) => spec.path === path)
    ) {
      throw new Error("A release can only change the configured version fields.");
    }
  }
  for (const spec of config.files) {
    const before = git(root, "show", `${sourceSha}:${spec.path}`);
    const after = git(root, "show", `${revision}:${spec.path}`);
    const expected = renderVersion(before, spec, tag.slice(1));
    const equal = spec.fields
      ? isDeepStrictEqual(JSON.parse(expected), JSON.parse(after))
      : expected === after;
    if (!equal) throw new Error(`Release changed more than the version in ${spec.path}.`);
  }
  if (
    requireTag &&
    (git(root, "cat-file", "-t", `refs/tags/${tag}`) !== "tag" ||
      git(root, "rev-parse", `refs/tags/${tag}^{commit}`) !== revision ||
      git(root, "for-each-ref", "--format=%(contents)", `refs/tags/${tag}`) !== `Release ${tag}`)
  ) {
    throw new Error("The annotated release tag does not match the request.");
  }
  return { tag, sourceSha };
}

export function ciApproved(run, jobs, sourceSha, repository) {
  if (
    run.head_sha !== sourceSha ||
    run.event !== "push" ||
    run.status !== "completed" ||
    run.conclusion !== "success" ||
    run.head_repository?.full_name !== repository ||
    (config.ciBranch && run.head_branch !== config.ciBranch) ||
    run.path?.split("@")[0] !== `.github/workflows/${config.workflow}`
  )
    return false;
  return (config.requiredJobs ?? ["CI approved"]).every((name) => {
    const matches = jobs.filter((job) => job.name === name);
    return (
      matches.length === 1 &&
      matches[0].status === "completed" &&
      matches[0].conclusion === "success" &&
      (!matches[0].head_sha || matches[0].head_sha === sourceSha) &&
      (!matches[0].run_id || matches[0].run_id === run.id)
    );
  });
}

export async function findApprovedCI({
  github,
  owner,
  repo,
  sourceSha,
  sleep = (ms) => new Promise((done) => setTimeout(done, ms)),
  attempts = 120,
}) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    const runs = await github.paginate(github.rest.actions.listWorkflowRuns, {
      owner,
      repo,
      workflow_id: config.workflow,
      head_sha: sourceSha,
      event: "push",
      per_page: 100,
    });
    // A release can arrive before GitHub has indexed the source push.
    let pending = !runs.some(
      (run) =>
        run.head_sha === sourceSha &&
        run.event === "push" &&
        run.head_repository?.full_name === `${owner}/${repo}` &&
        (!config.ciBranch || run.head_branch === config.ciBranch),
    );
    for (const run of runs) {
      if (
        run.head_sha !== sourceSha ||
        run.event !== "push" ||
        run.head_repository?.full_name !== `${owner}/${repo}` ||
        (config.ciBranch && run.head_branch !== config.ciBranch) ||
        run.path?.split("@")[0] !== `.github/workflows/${config.workflow}`
      )
        continue;
      if (run.status !== "completed") {
        pending = true;
        continue;
      }
      if (run.conclusion !== "success") continue;
      const jobs = await github.paginate(github.rest.actions.listJobsForWorkflowRun, {
        owner,
        repo,
        run_id: run.id,
        filter: "latest",
        per_page: 100,
      });
      if (ciApproved(run, jobs, sourceSha, `${owner}/${repo}`)) return run.id;
    }
    if (!pending || attempt === attempts - 1) break;
    await sleep(15000);
  }
  throw new Error(
    `Source ${sourceSha} has no successful push CI approval. Release stopped before building; fix or rerun that source CI, then rerun Release.`,
  );
}

export async function requireApprovedCI({
  github,
  owner,
  repo,
  revision,
  tag = "",
  root = process.cwd(),
  ...options
}) {
  const request = releaseRequest(root, revision, { requireTag: Boolean(tag), expectedTag: tag });
  if (tag && !request) throw new Error("Tag does not identify a version-only release request.");
  const sourceSha = request?.sourceSha ?? revision;
  const runId = await findApprovedCI({ github, owner, repo, sourceSha, ...options });
  return { sourceSha, runId };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, revision, tag] = process.argv.slice(2);
    const request = releaseRequest(process.cwd(), revision, {
      requireTag: command === "verify",
      expectedTag: tag,
    });
    if (command === "classify") console.log(request ? "release" : "full");
    else if (command === "verify" && request) console.log(JSON.stringify(request));
    else throw new Error("Usage: ci-policy.mjs classify SHA | verify SHA TAG");
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
