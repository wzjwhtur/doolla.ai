import { readFile, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import path from 'node:path';

const catalogDir = new URL('../../../src/doolla/internal/i18n/catalog/', import.meta.url);
const output = new URL('../src/i18n/catalog.generated.ts', import.meta.url);
const locales = ['en', 'zh-CN'];

export async function generateBrowserCatalog(check = false) {
  const catalogs = await Promise.all(locales.map(async locale => {
    const contents = await readFile(new URL(locale + '.json', catalogDir), 'utf8');
    return JSON.parse(contents);
  }));
  const keys = Object.keys(catalogs[0]).sort();
  const placeholders = message => (message.match(/\{[0-9]+\}/g) ?? []).sort().join(',');
  for (const [index, catalog] of catalogs.entries()) {
    if (Object.keys(catalog).sort().join('\n') !== keys.join('\n')) {
      throw new Error('Catalog keys differ for ' + locales[index]);
    }
    for (const key of keys) {
      if (typeof catalog[key] !== 'string' || !catalog[key] || placeholders(catalog[key]) !== placeholders(catalogs[0][key])) {
        throw new Error('Invalid translation in ' + locales[index] + ' for ' + key);
      }
    }
  }
  const browser = Object.fromEntries(catalogs.map((catalog, index) => [
    locales[index], Object.fromEntries(keys.filter(key => /^(web|common)\./.test(key)).map(key => [key, catalog[key]])),
  ]));
  const source = '// Generated from the shared Go locale catalogs.\nexport const catalogs = '
    + JSON.stringify(browser, null, 2) + ' as const;\n';
  const current = await readFile(output, 'utf8').catch(error => {
    if (error.code !== 'ENOENT') throw error;
    return '';
  });
  if (current === source) return;
  if (check) throw new Error('Stale browser translations. Run node frontends/webcomponent/scripts/generate-catalog.mjs.');
  await writeFile(output, source);
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  await generateBrowserCatalog(process.argv.includes('--check'));
}
