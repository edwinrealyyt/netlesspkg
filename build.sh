#!/usr/bin/env bash
# NetlessPkg 多平台构建脚本
# 用法: bash build.sh [clean]

set -e

VERSION="v0.1.0"
APP="netlesspkg"
OUTPUT_DIR="dist"

PLATFORMS=(
    "linux/amd64"
    "linux/arm64"
    "linux/arm"
    "windows/amd64"
)

if [ "$1" = "clean" ]; then
    echo "清理构建产物..."
    rm -rf "${OUTPUT_DIR}"
    exit 0
fi

mkdir -p "${OUTPUT_DIR}"

for platform in "${PLATFORMS[@]}"; do
    GOOS="${platform%/*}"
    GOARCH="${platform#*/}"
    output="${OUTPUT_DIR}/${APP}_${GOOS}_${GOARCH}"

    if [ "${GOOS}" = "windows" ]; then
        output="${output}.exe"
    fi

    echo "构建 ${GOOS}/${GOARCH} -> ${output}"
    CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -ldflags="-s -w" -o "${output}" .
done

echo ""
echo "构建完成:"
ls -lh "${OUTPUT_DIR}"/
