#!/bin/bash
set -e

cd "$(dirname "$0")"

VERSION=$(jq -r .version .claude-plugin/plugin.json)

echo "Building DuckDuckGo MCP Server v${VERSION} for all platforms..."

cd src

# Build for all platforms
platforms=(
    "darwin/amd64"
    "darwin/arm64"
    "linux/amd64"
    "linux/arm64"
)

for platform in "${platforms[@]}"; do
    IFS='/' read -r -a parts <<< "$platform"
    GOOS="${parts[0]}"
    GOARCH="${parts[1]}"

    output_name="ddg-search"

    # Map to npm platform naming
    npm_platform="$GOOS"
    npm_arch="$GOARCH"
    if [ "$GOARCH" = "amd64" ]; then
        npm_arch="x64"
    fi

    output_dir="../packages/ddg-search-${npm_platform}-${npm_arch}"

    echo "Building for ${GOOS}/${GOARCH}..."
    GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$VERSION" -o "$output_dir/$output_name" .
    chmod +x "$output_dir/$output_name"
done

echo ""
echo "Build complete! Binaries created in packages/ directories"
echo ""
echo "Binary sizes:"
for platform in "${platforms[@]}"; do
    IFS='/' read -r -a parts <<< "$platform"
    GOOS="${parts[0]}"
    GOARCH="${parts[1]}"

    npm_arch="$GOARCH"
    if [ "$GOARCH" = "amd64" ]; then
        npm_arch="x64"
    fi

    output_dir="../packages/ddg-search-${GOOS}-${npm_arch}"

    file="$output_dir/ddg-search"

    if [ -f "$file" ]; then
        size=$(du -h "$file" | cut -f1)
        echo "  ${GOOS}/${GOARCH}: ${size}"
    fi
done
