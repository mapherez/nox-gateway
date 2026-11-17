import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
if (!process.env.CI && !process.env.VERCEL && process.env.NOX_SKIP_HOOKS !== '1') {
  const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  let available = false;
  try { available = resolve(git('rev-parse', '--show-toplevel')) === root; } catch { /* Distribution without .git. */ }
  if (available) {
    let current = '';
    try { current = git('config', '--local', '--get', 'core.hooksPath'); } catch { /* Not configured. */ }
    if (current && !['.githooks', '.husky/_', '.husky'].includes(current)) {
      console.log(`Existing Git hooks retained (${current}). Project hooks are in .githooks.`);
    } else {
      git('config', '--local', 'core.hooksPath', '.githooks');
      console.log('Git hooks installed: staged style checks before commit; project checks before push.');
    }
  }
}
