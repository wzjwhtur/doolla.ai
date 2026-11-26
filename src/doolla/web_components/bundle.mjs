import { build } from 'esbuild';
import { gzipSync } from 'node:zlib';
import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.resolve(here, '../../../frontends/webcomponent');
const { version } = JSON.parse(await readFile(path.join(frontend, 'package.json'), 'utf8'));
const bundledPackages = [
  'lit', 'lit-html', 'lit-element', '@lit/reactive-element', 'plotly.js-dist-min',
  'markdown-it', 'entities', 'linkify-it', 'mdurl', 'punycode.js', 'uc.micro',
];
const licenseFiles = { 'punycode.js': 'LICENSE-MIT.txt', 'uc.micro': 'LICENSE.txt' };
const notices = await Promise.all(bundledPackages.map(async (name) => {
  const license = await readFile(path.join(here, 'node_modules', name, licenseFiles[name] ?? 'LICENSE'), 'utf8');
  return name + '\n' + license;
}));
const result = await build({
  absWorkingDir: here,
  entryPoints: [path.join(frontend, 'src/index.ts')],
  tsconfig: path.join(frontend, 'tsconfig.json'),
  // 前端开发目录也可能有 node_modules。内嵌资源始终从自己的锁文件取依赖。
  alias: {
    lit: './node_modules/lit',
    'markdown-it': './node_modules/markdown-it',
    'plotly.js-dist-min': './node_modules/plotly.js-dist-min',
  },
  bundle: true,
  minify: true,
  legalComments: 'inline',
  format: 'esm',
  banner: { js: '/*!\n' + notices.join('\n\n').replaceAll('*/', '* /') + '\n*/' },
  target: 'es2020',
  write: false,
  define: { __BUILD_VERSION__: JSON.stringify(version), __BUILD_TIME__: '"embedded"' },
});
await writeFile(path.join(here, 'doolla-components.js.gz'), gzipSync(result.outputFiles[0].contents, { level: 9 }));
