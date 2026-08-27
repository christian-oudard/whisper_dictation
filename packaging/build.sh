#!/bin/sh
# Build diktat against a source checkout of transcribe.cpp, for distributions
# that have no libtranscribe package.
#
# Nix does not use this: the flake takes libtranscribe from its own input and
# links against that. Everywhere else -- Arch, Debian, or a person building by
# hand -- the library has to be built first, and the two builds have to agree
# about where the headers and the archive are. That agreement is this file, so
# the PKGBUILD and debian/rules do not each carry their own copy of it and
# drift.
#
#   TRANSCRIBE_SRC   path to a transcribe.cpp checkout (required)
#   BUILD_DIR        where to build the library (default: $TRANSCRIBE_SRC/build)
#   OUT              where to write the binary (default: ./diktat)
#   VULKAN           1 to build the GPU backend, 0 for CPU only (default: 1)
#   VERSION          release this build calls itself (default: dev)
#
# The library is linked statically, so the result needs no libtranscribe at
# run time and a distribution needs to package only one thing.
set -eu

: "${TRANSCRIBE_SRC:?set TRANSCRIBE_SRC to a transcribe.cpp checkout}"
BUILD_DIR="${BUILD_DIR:-$TRANSCRIBE_SRC/build}"
OUT="${OUT:-./diktat}"
VULKAN="${VULKAN:-1}"

# TRANSCRIBE_VULKAN, not GGML_VULKAN: setting the ggml switch alone leaves the
# build CPU-only and says so nowhere except one line of cmake output.
cmake -S "$TRANSCRIBE_SRC" -B "$BUILD_DIR" \
    -DCMAKE_BUILD_TYPE=Release \
    -DTRANSCRIBE_VULKAN="$VULKAN" \
    -DTRANSCRIBE_BUILD_TESTS=OFF \
    -DTRANSCRIBE_BUILD_EXAMPLES=OFF
cmake --build "$BUILD_DIR" -j "$(nproc)"

# What to link against comes from the library's own manifest rather than from a
# list here: which ggml archives exist and which system libraries they need
# both depend on the backends enabled above, and getting that wrong shows up as
# a page of undefined references to vkCmdCopyBuffer.
manifest="$BUILD_DIR/transcribe-link.json"
[ -f "$manifest" ] || { echo "no $manifest; did the library build?" >&2; exit 1; }
system=$(sed -n 's/.*"system_libs": \[\(.*\)\].*/\1/p' "$manifest" | tr -d '"' | tr ',' ' ')
libs=$(find "$BUILD_DIR" -name '*.a' -printf '-l:%f ')
# Absolute, because the linker resolves -L against its own working directory
# and nothing promises that is this one.
dirs=$(find "$(cd "$BUILD_DIR" && pwd)" -name '*.a' -printf '-L%h\n' | sort -u | tr '\n' ' ')
for lib in $system; do
    libs="$libs -l$lib"
done

# The archives reference each other, and find lists them in whatever order the
# directory walk produces, so they go in a group rather than in a sequence the
# linker has to be lucky about.
CGO_CFLAGS="-I$TRANSCRIBE_SRC/include" \
CGO_LDFLAGS="$dirs -Wl,--start-group $libs -Wl,--end-group" \
    go build -trimpath \
        -ldflags "-X main.version=${VERSION:-dev} \
                  -X main.gitRev=${DIKTAT_REV:-unknown} \
                  -X main.gitDate=${DIKTAT_DATE:-}" \
        -o "$OUT" ./cmd/diktat

echo "built $OUT"
