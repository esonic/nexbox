#!/bin/bash

set -e  # Exit on any error

if [ $# -eq 0 ]; then
  echo "Usage: $0 <tk_value>"
  exit 1
fi

tk_value=$1

# Generate version based on current timestamp
version=$(date +%Y%m%d%H%M)
echo "Version: $version"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Build frontend
echo "Building frontend..."
cd "$SCRIPT_DIR/web"
npm run build
cd "$SCRIPT_DIR"

# Copy web dist into server for Go embed
echo "Copying web dist to server..."
rm -rf "$SCRIPT_DIR/server/webdist"
cp -r "$SCRIPT_DIR/web/dist" "$SCRIPT_DIR/server/webdist"

# Function to build, compress, split and upload boxnex binary
# Args: $1=platform, $2=arch
upload_boxnex() {
  local platform=$1
  local arch=$2

  echo "=== Processing $platform/$arch ==="

  local binary_name="boxnex-${platform}-${arch}"

  # Build
  echo "Building boxnex for $platform/$arch..."
  cd "$SCRIPT_DIR/server"
  GOOS=$platform GOARCH=$arch go build -ldflags "-s -w -X main.Version=$version" -o "$SCRIPT_DIR/$binary_name"
  cd "$SCRIPT_DIR"

  # Get original file size
  original_size=$(wc -c < "$binary_name")
  original_size_mb=$(echo "scale=2; $original_size / 1024 / 1024" | bc)
  echo "Binary size: ${original_size_mb} MB"

  echo "Compressing with gzip..."
  gzip -9 -k "$binary_name"

  local compressed_file="${binary_name}.gz"

  if [ ! -f "$compressed_file" ]; then
    echo "Error: $compressed_file not found!"
    exit 1
  fi

  # Get compressed file size and calculate compression ratio
  compressed_size=$(wc -c < "$compressed_file")
  compressed_size_mb=$(echo "scale=2; $compressed_size / 1024 / 1024" | bc)
  ratio=$(echo "scale=1; 100 * $compressed_size / $original_size" | bc)
  echo "Compressed size: ${compressed_size_mb} MB (${ratio}% of original)"

  # Split file into chunks
  echo "Splitting file into chunks..."
  split -b 1m -a 2 -d "$compressed_file" "chunk_${platform}_${arch}_"

  # Count chunks
  chunk_files=(chunk_${platform}_${arch}_*)
  num_chunks=${#chunk_files[@]}
  echo "Created $num_chunks chunks"

  # Upload all chunks
  for ((i=1; i<=num_chunks; i++)); do
    echo "Uploading chunk $i/$num_chunks..."

    chunk_file=$(printf "chunk_${platform}_${arch}_%02d" $((i-1)))

    if [ $i -eq 1 ]; then
      append_param=""
    else
      append_param="&append=true"
    fi

    response=$(curl -s -w "%{http_code}" "https://sre-admin.okg.com/rca/api/v1/oss/put_boxnex?tk=${tk_value}&version=${version}&platform=${platform}&arch=${arch}${append_param}" -F "file=@${chunk_file}")
    http_code="${response: -3}"

    if [ "$http_code" != "200" ]; then
      echo "Error: Chunk $i upload failed with HTTP code $http_code"
      echo "Response: $response"
      exit 1
    fi
  done

  # Clean up this platform's temporary files
  echo "Cleaning up temporary files for $platform/$arch..."
  rm -f chunk_${platform}_${arch}_*
  rm -f "$compressed_file"
  rm -f "$binary_name"

  echo "=== $platform/$arch upload completed successfully! ==="
}

upload_boxnex "darwin" "arm64"
upload_boxnex "linux" "amd64"

echo "============================================"
echo "All platforms uploaded successfully!"
echo "Version: $version"
echo "============================================"
