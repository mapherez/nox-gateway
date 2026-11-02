import { execFileSync, spawn } from 'node:child_process';
import { readFile, writeFile, realpath } from 'node:fs/promises';
import { realpathSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export function normalizeVersion(input) {
  const version = typeof input === 'string' ? input.replace(/^v/, '') : '';
  // JS $ also matches before a final newline; reject any whitespace explicitly.
  if (!semver.test(version) || /\s/.test(version)) {
    throw new Error('Versao invalida. Usa X.Y.Z ou X.Y.Z-prerelease.');
  }
  if (version.includes('+')) {
    throw new Error('Metadados de build (+) nao sao permitidos nas tags Docker. Usa X.Y.Z ou X.Y.Z-prerelease.');
  }
  if (`v${version}`.length > 128) throw new Error('A tag Docker excede 128 caracteres.');
  return version;
}

export function parsePackageVersion(contents) {
  const version = JSON.parse(contents)?.version;
  if (typeof version !== 'string' || version.startsWith('v') || normalizeVersion(version) !== version) {
    throw new Error('package.json deve conter uma version SemVer sem prefixo v.');
  }
  return version;
}

export function validateReleaseTag(tag, contents) {
  if (typeof tag !== 'string' || !tag.startsWith('v')) {
    throw new Error('A tag de release deve ser SemVer com prefixo v.');
  }
  const version = normalizeVersion(tag);
  if (`v${version}` !== tag || parsePackageVersion(contents) !== version) {
    throw new Error(`package.json nao corresponde exactamente a ${tag}.`);
  }
  return version;
}

export async function runReleaseCLI(root, args, {
  input = process.stdin, output = process.stdout, publish = release,
} = {}) {
  if (args.length > 1) throw new Error('Uso: npm run release [-- X.Y.Z]');
  const current = parsePackageVersion(await readFile(resolve(root, 'package.json'), 'utf8'));
  output.write(`Versao atual: ${current}\n`);
  let version = args[0];
  if (version === undefined) {
    const prompt = createInterface({ input, output });
    try {
      version = await new Promise((resolveAnswer, reject) => {
        prompt.once('close', () => reject(new Error('Release cancelada. Nenhuma versao indicada.')));
        prompt.once('SIGINT', () => reject(new Error('Release cancelada.')));
        prompt.once('error', reject);
        prompt.question('Nova versao (X.Y.Z): ', answer => resolveAnswer(answer.trim()));
      });
    } finally {
      prompt.close();
    }
  }
  return publish(root, normalizeVersion(version));
}

export function runReleaseCommand(command, args, options) {
  return new Promise((resolveCommand, reject) => {
    const child = spawn(command, args, { ...options, stdio: ['inherit', 'inherit', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', chunk => {
      process.stderr.write(chunk);
      stderr = (stderr + chunk.toString()).slice(-65536);
    });
    child.once('error', reject);
    child.once('close', (code, signal) => {
      if (code === 0) return resolveCommand();
      const error = new Error(`Command failed: ${command} ${args.join(' ')} (${signal || code})`);
      error.stderr = stderr;
      reject(error);
    });
  });
}

export async function runReleaseChecks(root, tag, run = runReleaseCommand) {
  await run('go', ['vet', '-mod=readonly', './...'], { cwd: root });
  await run('go', ['test', '-race', '-mod=readonly', './...'], { cwd: root });
  await run('docker', ['build', '--build-arg', `VERSION=${tag}`, '-t', 'nox-gateway:release-check', '.'], { cwd: root });
  await run('python3', ['scripts/check-image.py', '--image', 'nox-gateway:release-check', '--version', tag], { cwd: root });
}

export async function release(root, input, { check = runReleaseChecks } = {}) {
  const version = normalizeVersion(input);
  const tag = `v${version}`;
  const git = (...args) => execFileSync('git', args, {
    cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
  if (await realpath(git('rev-parse', '--show-toplevel')) !== await realpath(root)) {
    throw new Error('Executa o script na root do repositorio nox-gateway.');
  }
  if (git('status', '--porcelain', '--untracked-files=all')) {
    throw new Error('A arvore Git tem alteracoes. Faz commit ou stash antes da release.');
  }
  const branch = git('symbolic-ref', '--quiet', '--short', 'HEAD');
  const packagePath = resolve(root, 'package.json');
  const originalPackage = await readFile(packagePath, 'utf8');
  const current = parsePackageVersion(originalPackage);
  const manifest = JSON.parse(originalPackage);
  if (current === version) throw new Error('package.json ja usa essa versao; escolhe uma nova versao.');
  if (git('tag', '--list', tag)) throw new Error(`A tag ${tag} ja existe localmente.`);
  // A failed remote lookup must abort too; never assume an unreachable origin has no tag.
  if (git('ls-remote', '--tags', 'origin', `refs/tags/${tag}`, `refs/tags/${tag}^{}`)) {
    throw new Error(`A tag ${tag} ja existe em origin.`);
  }
  git('var', 'GIT_AUTHOR_IDENT');
  git('var', 'GIT_COMMITTER_IDENT');
  const head = git('rev-parse', 'HEAD');
  try {
    manifest.version = version;
    const indent = originalPackage.match(/\n([\t ]+)"/)?.[1] ?? 2;
    const newline = originalPackage.includes('\r\n') ? '\r\n' : '\n';
    const updatedPackage = JSON.stringify(manifest, null, indent).replaceAll('\n', newline);
    await writeFile(packagePath, updatedPackage + (originalPackage.endsWith('\n') ? newline : ''));
    await check(root, tag);
    validateReleaseTag(tag, await readFile(packagePath, 'utf8'));
    if (git('rev-parse', 'HEAD') !== head || git('symbolic-ref', '--quiet', '--short', 'HEAD') !== branch ||
        git('diff', '--name-only').split('\n').filter(Boolean).some(file => file !== 'package.json') ||
        git('diff', '--cached', '--name-only') || git('ls-files', '--others', '--exclude-standard')) {
      throw new Error('A validacao alterou o repositorio fora de package.json. Reve as alteracoes antes de publicar.');
    }
    git('add', '--', 'package.json');
    git('commit', '-m', `chore: release ${tag}`);
  } catch (error) {
    // Restore our edit only if a release commit was not created. Keep unrelated changes.
    if (git('rev-parse', 'HEAD') === head) {
      await writeFile(packagePath, originalPackage);
      git('restore', '--staged', '--', 'package.json');
    }
    throw error;
  }
  const pushArgs = ['push', '--atomic', 'origin', `HEAD:refs/heads/${branch}`, `refs/tags/${tag}:refs/tags/${tag}`];
  const quote = arg => `'${arg.replaceAll("'", process.platform === 'win32' ? "''" : "'\\''")}'`;
  const recovery = `git push --atomic origin ${pushArgs.slice(3).map(quote).join(' ')}`;
  try {
    git('tag', '-a', tag, '-m', `Release ${tag}`);
  } catch (error) {
    throw new Error(`Commit de release mantido localmente. A tag falhou. Recovery: git tag -a ${tag} -m "Release ${tag}"; depois ${recovery}`, { cause: error });
  }
  try {
    git(...pushArgs);
  } catch (error) {
    throw new Error(`Push falhou. O commit e a tag ${tag} ficaram locais. Corrige a causa e repete: ${recovery}`, { cause: error });
  }
  return tag;
}

export function isMainScript(url) {
  if (!process.argv[1]) return false;
  try { return realpathSync(resolve(process.argv[1])) === realpathSync(fileURLToPath(url)); }
  catch { return false; }
}

if (isMainScript(import.meta.url)) {
  (async () => {
    const tag = await runReleaseCLI(repoRoot, process.argv.slice(2));
    console.log(`Commit e tag ${tag} publicados. O GitHub Actions vai publicar a imagem e criar a release.`);
    console.log(`https://github.com/mapherez/nox-gateway/actions/workflows/release.yml`);
  })().catch(error => {
    console.error(error.stderr?.toString().trim() || error.message);
    if (error.cause) console.error(error.cause.stderr?.toString().trim() || error.cause.message);
    process.exitCode = 1;
  });
}
