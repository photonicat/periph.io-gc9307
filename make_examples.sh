#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}GC9307 Sample Program Build Script${NC}"
echo "=================================="

# Detect host architecture
HOST_ARCH=$(uname -m)
echo "Host architecture: $HOST_ARCH"

# Check if example.png exists
if [ ! -f "example.png" ]; then
    echo -e "${YELLOW}Warning: example.png not found in root folder. Make sure to place a test image file.${NC}"
fi

# Export PATH for musl cross-compiler
export PATH=/usr/local/aarch64-linux-musl-cross/bin:$PATH

if [ "$HOST_ARCH" = "x86_64" ]; then
    echo -e "\n${YELLOW}Cross-compiling for OpenWrt aarch64...${NC}"
    # Cross-compile for OpenWrt using musl
    BUILD_ENV="GOOS=linux GOARCH=arm64 CGO_ENABLED=1"
    CC="aarch64-linux-musl-gcc"
    
    if ! command -v "$CC" >/dev/null 2>&1; then
        echo -e "${RED}Error: $CC not found${NC}"
        echo -e "Install musl cross-compiler with:"
        echo -e "  ${BLUE}wget https://musl.cc/aarch64-linux-musl-cross.tgz${NC}"
        echo -e "  ${BLUE}sudo tar -C /usr/local -xzf aarch64-linux-musl-cross.tgz${NC}"
        exit 1
    fi
    
    echo "Using cross-compiler: $CC"
    echo "Building sample..."
    cd examples/sample && env $BUILD_ENV CC=$CC go build -buildvcs=false -o ../../gc9307_sample . && cd ../..
    
    echo "Building benchmark..."
    cd examples/benchmark && env $BUILD_ENV CC=$CC go build -buildvcs=false -o ../../gc9307_benchmark . && cd ../..
else
    echo -e "\n${YELLOW}Native compilation for ARM...${NC}"
    # Native compile on ARM system
    echo "Building sample..."
    cd examples/sample && go build -buildvcs=false -o ../../gc9307_sample . && cd ../..
    
    echo "Building benchmark..."
    cd examples/benchmark && go build -buildvcs=false -o ../../gc9307_benchmark . && cd ../..
fi

if [ $? -eq 0 ]; then
    echo -e "${GREEN}Build successful!${NC}"
    echo ""
    echo "Built binaries:"
    echo "- ${BLUE}./gc9307_sample${NC}  - Basic sample program"
    echo "- ${BLUE}./gc9307_benchmark${NC} - Performance benchmark with 3x3 panning"
    echo ""
    echo "Sample Program Features:"
    echo "- Basic GC9307 display initialization"
    echo "- Image loading and display"
    echo "- Example of driver usage patterns"
    echo ""
    echo "Benchmark Program Features:"
    echo "- 3x3 grid of example.png with pixel-by-pixel panning"
    echo "- Real-time FPS measurement and console output"
    echo "- DMA-optimized transfers (enabled by default)"
    echo "- Configurable benchmark duration and DMA mode"
    echo "- Usage: ./gc9307_benchmark [-nodma] [-duration=30]"
    echo ""
    echo "Make sure to run these on your target device with the GC9307 display connected."
else
    echo -e "${RED}Build failed. Check your Go installation and dependencies.${NC}"
    exit 1
fi