#!/usr/bin/env bash
# Build a relocatable source distribution with the SDK and optional driver modules.
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p dist
archive=$(mktemp dist/.doolla-source.XXXXXXXX)
trap 'rm -f "$archive"' EXIT

{
  printf '%s\0' go.mod go.sum docs/guide/{Go模块打包与安装,配置参考}.md \
    frontends/webcomponent/package.json frontends/webcomponent/tsconfig.json \
    frontends/webcomponent/scripts/generate-catalog.mjs \
    src/doolla/internal/i18n/catalog/{en,zh-CN}.json \
    src/doolla/web_components/{api.html,openapi.json,doolla-components.js.gz,bundle.mjs,package.json,package-lock.json}
  find src/doolla -name node_modules -prune -o -type f \
    \( -name '*.go' -o -name go.mod -o -name go.sum \) ! -name '*_test.go' -print0
  find frontends/webcomponent/src -type f -name '*.ts' ! -name '*.stories.ts' -print0
} | COPYFILE_DISABLE=1 tar -czf "$archive" --null -T -

chmod 644 "$archive"
mv "$archive" dist/doolla-source.tar.gz
echo "dist/doolla-source.tar.gz"
