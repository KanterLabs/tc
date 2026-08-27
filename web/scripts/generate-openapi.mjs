import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { parse } from 'yaml';

const here = dirname(fileURLToPath(import.meta.url));
const sourcePath = resolve(here, '../../openapi.yaml');
const outputPath = resolve(here, '../../internal/httpapi/openapi.json');
const generated = `${JSON.stringify(parse(await readFile(sourcePath, 'utf8')), null, 2)}\n`;

if (process.argv.includes('--check')) {
  const existing = await readFile(outputPath, 'utf8').catch(() => '');
  if (existing !== generated) {
    console.error('internal/httpapi/openapi.json is stale; run npm run openapi:generate');
    process.exitCode = 1;
  }
} else {
  await writeFile(outputPath, generated);
}
