import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { config, git, versionTag, renderVersion, releaseRequest, ciApproved, findApprovedCI } from './ci-policy.mjs';

const source = fileURLToPath(new URL('../', import.meta.url));
function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'ci-reuse-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  git(root, 'init', '-b', 'main');
  for (const [key, value] of Object.entries({ 'user.name': 'CI Test', 'user.email': 'ci@example.invalid',
    'commit.gpgsign': 'false', 'core.hooksPath': join(root, 'no-hooks') })) git(root, 'config', key, value);
  for (const spec of config.files) {
    const path = join(root, spec.path);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, readFileSync(join(source, spec.path)));
  }
  writeFileSync(join(root, 'source.txt'), 'source\n');
  git(root, 'add', '.'); git(root, 'commit', '-m', 'source');
  const sourceSha = git(root, 'rev-parse', 'HEAD');
  return { root, sourceSha, commit({ extra = false, mode = false, marker = true } = {}) {
    for (const spec of config.files) {
      const path = join(root, spec.path);
      writeFileSync(path, renderVersion(readFileSync(path, 'utf8'), spec, '99.0.0'));
    }
    if (extra) writeFileSync(join(root, 'source.txt'), 'changed\n');
    git(root, 'add', '.');
    if (mode) git(root, 'update-index', '--chmod=+x', 'package.json');
    const body = marker ? `${config.marker} ${config.markerVersionOnly ? '99.0.0' : 'v99.0.0'}` : 'ordinary change';
    git(root, 'commit', '-m', 'chore: release v99.0.0', '-m', body);
    return git(root, 'rev-parse', 'HEAD');
  } };
}

test('release proof accepts only configured version changes and a matching annotated tag', t => {
  const f = fixture(t), revision = f.commit();
  assert.deepEqual(releaseRequest(f.root, revision), { tag: 'v99.0.0', sourceSha: f.sourceSha });
  assert.throws(() => releaseRequest(f.root, revision, { requireTag: true }));
  git(f.root, 'tag', 'v99.0.0');
  assert.throws(() => releaseRequest(f.root, revision, { requireTag: true }));
  git(f.root, 'tag', '-d', 'v99.0.0');
  git(f.root, 'tag', '-a', 'v99.0.0', '-m', 'Release v99.0.0');
  assert.equal(releaseRequest(f.root, revision, { requireTag: true, expectedTag: 'v99.0.0' }).sourceSha, f.sourceSha);
  assert.throws(() => releaseRequest(f.root, revision, { expectedTag: 'v99.0.1' }));
});

test('source changes, executable mode changes and manifest script changes cannot reuse CI', t => {
  for (const option of [{ extra: true }, { mode: true }]) {
    const f = fixture(t); assert.throws(() => releaseRequest(f.root, f.commit(option)));
  }
  const f = fixture(t); f.commit();
  const path = join(f.root, 'package.json'), value = JSON.parse(readFileSync(path));
  value.scripts = { injected: 'echo changed' }; writeFileSync(path, JSON.stringify(value));
  git(f.root, 'add', '.'); git(f.root, 'commit', '--amend', '--no-edit');
  assert.throws(() => releaseRequest(f.root, git(f.root, 'rev-parse', 'HEAD')));
});

test('ordinary commits run full CI and versions reject malformed SemVer', t => {
  const f = fixture(t); assert.equal(releaseRequest(f.root, f.sourceSha), null);
  assert.equal(releaseRequest(f.root, f.commit({ marker: false })), null);
  for (const v of ['1.2', '1.02.0', '1.2.3+build', '1.2.3\n', '1.2.3-01']) assert.throws(() => versionTag(v));
  assert.equal(versionTag('1.2.3-rc.1'), 'v1.2.3-rc.1');
});

function evidence() {
  const sourceSha = 'a'.repeat(40), repository = 'example/project';
  const run = { id: 42, head_sha: sourceSha, head_branch: config.ciBranch ?? 'main', event: 'push', status: 'completed', conclusion: 'success',
    head_repository: { full_name: repository }, path: `.github/workflows/${config.workflow}` };
  const jobs = (config.requiredJobs ?? ['CI approved']).map(name => ({ name, status: 'completed', conclusion: 'success',
    head_sha: sourceSha, run_id: 42 }));
  return { sourceSha, repository, run, jobs };
}

test('approval requires successful unskipped jobs for the exact push, repository and workflow', () => {
  const { run, jobs, sourceSha, repository } = evidence();
  assert.equal(ciApproved(run, jobs, sourceSha, repository), true);
  for (const patch of [{ event: 'pull_request' }, { event: 'workflow_dispatch' }, { head_sha: 'b'.repeat(40) },
    { conclusion: 'failure' }, { status: 'in_progress' }, { path: '.github/workflows/other.yml' },
    { head_repository: { full_name: 'fork/project' } }]) assert.equal(ciApproved({ ...run, ...patch }, jobs, sourceSha, repository), false);
  for (const conclusion of ['skipped', 'failure', 'cancelled']) {
    assert.equal(ciApproved(run, [{ ...jobs[0], conclusion }, ...jobs.slice(1)], sourceSha, repository), false);
  }
  assert.equal(ciApproved(run, [], sourceSha, repository), false);
  assert.equal(ciApproved(run, [...jobs, jobs[0]], sourceSha, repository), false);
});

test('pending source CI is awaited; failed CI stops release without rerunning tests', async () => {
  const { run, jobs, sourceSha } = evidence();
  let count = 0, waits = 0;
  const github = { rest: { actions: { listWorkflowRuns: 'runs', listJobsForWorkflowRun: 'jobs' } },
    paginate: async endpoint => endpoint === 'jobs' ? jobs : ++count === 1 ? [] : [run] };
  const options = { github, owner: 'example', repo: 'project', sourceSha, attempts: 2, sleep: async () => { waits++; } };
  assert.equal(await findApprovedCI(options), 42); assert.equal(waits, 1);
  run.conclusion = 'failure';
  await assert.rejects(findApprovedCI(options), /no successful push CI/);
});
