// Run with "node frontends/webcomponent/tests/run-browser-tests.mjs" after npm ci in src/doolla/web_components.
import { execFileSync } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { generateBrowserCatalog } from '../scripts/generate-catalog.mjs';

await generateBrowserCatalog(true);

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.dirname(here);
const modules = path.resolve(frontend, '../../src/doolla/web_components/node_modules');
const { build } = createRequire(path.resolve(modules, '../package.json'))('esbuild');
const tmp = await mkdtemp(path.join(tmpdir(), 'doolla-frontend-test-'));
const chrome = process.env.CHROME_BIN || (process.platform === 'darwin'
  ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'google-chrome');
try {
  await build({
    entryPoints: [path.join(here, 'transport.test.ts')], bundle: true, target: 'es2020',
    tsconfig: path.join(frontend, 'tsconfig.json'), nodePaths: [modules],
    outfile: path.join(tmp, 'test.js'), format: 'iife',
  });
  const page = path.join(tmp, 'test.html');
  await writeFile(page, '<meta charset="utf-8"><body><script src="test.js"></script></body>');
  const html = execFileSync(chrome, ['--headless', '--disable-gpu', '--disable-extensions',
    '--user-data-dir=' + path.join(tmp, 'profile'), '--virtual-time-budget=10000',
    '--dump-dom', pathToFileURL(page).href], { encoding: 'utf8', timeout: 45000 });
  if (!html.includes('data-test-result="passed"')) {
    const failure = html.match(/<body[^>]*data-test-result="failed"[^>]*>([\s\S]*?)<\/body>/);
    throw new Error(failure?.[1] ?? 'Browser stopped before reporting a test result');
  }
  console.log('Passed browser transport, component and Markdown regression tests');
} finally {
  await rm(tmp, { recursive: true, force: true, maxRetries: 3 });
}
