import { readFile } from 'node:fs/promises';
import { validateReleaseTag } from './release.mjs';

try {
  if (process.argv.length !== 3) throw new Error('Uso: node scripts/check-release-version.mjs vX.Y.Z');
  validateReleaseTag(process.argv[2], await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  console.log(`package.json corresponde a ${process.argv[2]}.`);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
