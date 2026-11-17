import { execFileSync, spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const config = JSON.parse(readFileSync(new URL('./dev-checks.json', import.meta.url), 'utf8'));
process.chdir(root);
const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
function run(command, args, cwd = root, input) {
  const result = spawnSync(command, args, { cwd, input, encoding: 'utf8', stdio: input === undefined ? 'inherit' : ['pipe', 'pipe', 'inherit'],
    shell: process.platform === 'win32' && command === 'npm' });
  if (result.error || result.status !== 0) throw new Error(result.error?.message ?? `${command} failed (${result.status}).`);
  return result.stdout;
}
const npm = (args, cwd) => run('npm', args, cwd);

async function releaseOnlyPush() {
  if (process.stdin.isTTY) return false;
  const refs = readFileSync(0, 'utf8').trim().split('\n').filter(Boolean).map(line => line.split(' '));
  if (!refs.length) return false;
  const sha = git('rev-parse', 'HEAD').trim();
  // A multi-ref push can also contain unrelated work. Only bypass local source
  // checks when every pushed ref resolves to this one metadata-only commit.
  if (refs.some(([, localSha]) => /^0+$/.test(localSha) || git('rev-parse', `${localSha}^{commit}`).trim() !== sha)) return false;
  const policy = await import(pathToFileURL(join(root, config.policy)).href);
  if (config.yard) {
    const { requestedRelease } = await import(pathToFileURL(join(root, 'scripts/release-request.mjs')).href);
    const ref = refs.find(([localRef]) => localRef.startsWith('refs/heads/'))?.[0];
    if (!ref || !requestedRelease({ revision: sha, ref, event: 'push' })) return false;
    policy.assertVersionOnly(sha);
    return true;
  }
  return Boolean(policy.releaseRequest(root, sha));
}

async function main() {
  if (process.env.NOX_SKIP_HOOKS === '1') return;
  const mode = process.argv[2];
  if (mode === 'commit') {
    git('diff', '--cached', '--check');
    const files = git('diff', '--cached', '--name-only', '--diff-filter=ACMR', '-z').split('\0').filter(Boolean);
    for (const path of files.filter(path => path.endsWith('.go'))) {
      const contents = git('show', `:${path}`);
      if (run('gofmt', [], root, contents) !== contents) throw new Error(`${path} needs gofmt. Run npm run format and stage the result.`);
    }
    for (const folder of config.vite ?? []) {
      if (files.some(path => folder === "." || path.startsWith(`${folder}/`))) npm(['exec', '--no', '--', 'vp', 'staged'], join(root, folder));
    }
    if (config.husky && files.some(path => /\.(?:[cm]?js|jsx|tsx?)$/.test(path))) run('sh', ['.husky/pre-commit']);
    if (files.some(path => path.endsWith('.py')) && config.python) {
      run('uv', ['run', '--frozen', 'ruff', 'check', ...files.filter(path => path.endsWith('.py'))]);
    }
    if (files.some(path => path.endsWith('.scss')) && config.styles) npm(['run', 'check:styles']);
    if (config.impactedTests && files.some(path => /\.(?:[cm]?js|jsx|tsx?)$/.test(path))) run(process.execPath, ["scripts/run-impacted-tests.mjs"]);
  } else if (mode === 'push') {
    if (await releaseOnlyPush()) {
      console.log('Version-only release: GitHub will verify and reuse approved source CI.');
      return;
    }
    npm(['run', 'prepush']);
  } else if (mode === 'check' || mode === 'format') {
    if (config.go) {
      const files = git('ls-files', '-z', '--', '*.go').split('\0').filter(Boolean);
      if (files.length) {
        if (mode === 'format') run('gofmt', ['-w', ...files]);
        else {
          const unformatted = execFileSync('gofmt', ['-l', ...files], { cwd: root, encoding: 'utf8' });
          if (unformatted.trim()) throw new Error(`Run npm run format:\n${unformatted}`);
        }
      }
    }
    for (const task of config[mode] ?? []) run(task.command, task.args, resolve(root, task.cwd ?? '.'));
  } else throw new Error('Usage: git-hooks.mjs commit | push | check | format');
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
