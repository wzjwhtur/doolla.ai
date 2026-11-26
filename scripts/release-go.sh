#!/usr/bin/env bash
# Prepare a versioned Go proxy and, on macOS, a native service installer.
set -euo pipefail
cd "$(dirname "$0")/.."
repo=$(pwd -P)
version=${1:-}
if [[ ! $version =~ ^(0|1)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ || $# != 1 ]]; then
  echo "usage: $0 0.0.1 (three numeric components, major 0 or 1)" >&2
  exit 2
fi
tag="v$version"
# Go 用模块路径和版本号定位缓存；复用上游 Python 标签会使已有缓存的消费者
# 读到同名 Python 源码，即使后来切换到本地模块代理也是如此。
if git cat-file -e "refs/tags/$tag:pyproject.toml" 2>/dev/null &&
   ! git cat-file -e "refs/tags/$tag:src/doolla/go.mod" 2>/dev/null; then
  echo "$tag is already a Python release; choose a separate Go module version" >&2
  exit 1
fi
dest="$repo/dist/releases/$tag"
mkdir -p "$repo/dist/releases"
if [[ -e $dest ]]; then echo "release already exists: $dest" >&2; exit 1; fi
# Use a private lock and stage on the same filesystem for atomic publication.
lock="$dest.lock"
mkdir "$lock" || exit 1
trap 'rm -r "$lock"' EXIT
if [[ -e $dest ]]; then echo "release already exists: $dest" >&2; exit 1; fi
tmp="$lock/work"
out="$lock/output"
mkdir -p "$tmp/source" "$out/proxy"
./scripts/package-go.sh
tar -xzf dist/doolla-source.tar.gz -C "$tmp/source"

sdk_source="$tmp/source/src/doolla"
payload="$tmp/payload"
mkdir -p "$payload/bin"
platform=$(uname -s)
# Build from the extracted distribution so the checkout cannot supply missing assets.
if [[ $platform == Darwin ]]; then
  arch=$(go env GOHOSTARCH)
  GOWORK=off GOOS=darwin GOARCH="$arch" CGO_ENABLED=1 go -C "$sdk_source" build -mod=readonly -trimpath -o "$payload/bin/doolla" ./cmd/doolla
else
  GOWORK=off go -C "$sdk_source" build -mod=readonly -trimpath -o "$payload/bin/doolla" ./cmd/doolla
fi

sdk=doolla.ai/sdk
# Separate nested modules before zipping the parent. Only staged copies are edited.
mv "$sdk_source/integrations/chromalocal" "$tmp/chromalocal"
mv "$sdk_source/integrations/database/drivers" "$tmp/drivers"
built=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
publish_module() {
  local dir=$1 mod=$2 target="$out/proxy/$2/@v"
  local prefix="$mod@$tag"
  mkdir -p "$target" "$tmp/zip/$(dirname "$prefix")"
  if [[ $mod != "$sdk" ]]; then
    GOWORK=off go -C "$dir" mod edit "-dropreplace=$sdk" "-require=$sdk@$tag"
  fi
  cp "$dir/go.mod" "$target/$tag.mod"
  printf '{"Version":"%s","Time":"%s"}\n' "$tag" "$built" > "$target/$tag.info"
  printf '%s\n' "$tag" > "$target/list"
  mv "$dir" "$tmp/zip/$prefix"
  (cd "$tmp/zip" && zip -q -X -r -D "$target/$tag.zip" "$prefix")
}
publish_module "$sdk_source" "$sdk"
publish_module "$tmp/drivers" "$sdk/integrations/database/drivers"
publish_module "$tmp/chromalocal" "$sdk/integrations/chromalocal"
COPYFILE_DISABLE=1 tar -czf "$out/doolla-module-proxy-$tag.tar.gz" -C "$out" proxy

if [[ $platform == Darwin ]]; then
  sign_args=()
  if [[ -n ${MAC_APP_IDENTITY:-} || -n ${MAC_INSTALLER_IDENTITY:-} ]]; then
    : "${MAC_APP_IDENTITY:?set both Developer ID identities}" "${MAC_INSTALLER_IDENTITY:?set both Developer ID identities}"
    codesign --force --sign "$MAC_APP_IDENTITY" --options runtime --timestamp "$payload/bin/doolla"
    sign_args=(--sign "$MAC_INSTALLER_IDENTITY" --timestamp)
  fi
  pkgbuild --root "$payload" --install-location /usr/local --ownership recommended \
    --identifier local.doolla.go.service --version "$version" ${sign_args[@]+"${sign_args[@]}"} "$out/doolla-$tag-macos-$arch.pkg"
fi
(cd "$out" && for file in *.tar.gz *.pkg; do
  if [[ -f $file ]]; then shasum -a 256 "$file"; fi
done > SHA256SUMS)
mv "$out" "$dest"
echo "$dest"
