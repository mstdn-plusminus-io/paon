#!/bin/bash

# Setup script for macOS dependencies
# This script installs required dependencies for Mastodon on macOS using Homebrew

set -e

echo "🔧 Setting up macOS dependencies for Mastodon..."

# Check if running on macOS
if [[ "$(uname)" != "Darwin" ]]; then
    echo "This script is only for macOS. Skipping..."
    exit 0
fi

# Check if Homebrew is installed
if ! command -v brew &> /dev/null; then
    echo "❌ Homebrew is not installed. Please install it first:"
    echo "   /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
    exit 1
fi

echo "📦 Checking and installing required Homebrew packages..."

# Install required packages
packages=(
    "icu4c"
    "pkg-config"
    "imagemagick"
    "ffmpeg"
    "libidn"
    "libpq"
    "redis"
    "postgresql@14"
)

for package in "${packages[@]}"; do
    if brew list --formula | grep -q "^${package}\$"; then
        echo "✅ ${package} is already installed"
    else
        echo "📥 Installing ${package}..."
        brew install "${package}"
    fi
done

# Set up environment variables for bundle install
echo ""
echo "🔧 Setting up environment variables..."

# Detect Homebrew prefix (different for Intel vs Apple Silicon Macs)
BREW_PREFIX=$(brew --prefix)
ICU4C_PREFIX="${BREW_PREFIX}/opt/icu4c"
LIBPQ_PREFIX="${BREW_PREFIX}/opt/libpq"

# For charlock_holmes 0.7.9+, icu4c@75 works fine
# If we have icu4c@77, we should use icu4c@75 instead
if [[ -d "${BREW_PREFIX}/opt/icu4c@77" ]]; then
    echo "⚠️  Detected icu4c@77, switching to icu4c@75 for charlock_holmes compatibility"

    # Make sure icu4c@75 is installed
    if ! [[ -d "${BREW_PREFIX}/opt/icu4c@75" ]]; then
        echo "📦 Installing icu4c@75..."
        brew install --force icu4c@75
    fi

    # Link icu4c@75
    brew unlink icu4c@77 2>/dev/null || true
    brew link --force icu4c@75

    ICU4C_PREFIX="${BREW_PREFIX}/opt/icu4c@75"
elif [[ -d "${BREW_PREFIX}/opt/icu4c@75" ]]; then
    ICU4C_PREFIX="${BREW_PREFIX}/opt/icu4c@75"
fi

# Export environment variables
export PKG_CONFIG_PATH="${ICU4C_PREFIX}/lib/pkgconfig:${LIBPQ_PREFIX}/lib/pkgconfig:${PKG_CONFIG_PATH}"
export LDFLAGS="-L${ICU4C_PREFIX}/lib -L${LIBPQ_PREFIX}/lib ${LDFLAGS}"
export CPPFLAGS="-I${ICU4C_PREFIX}/include -I${LIBPQ_PREFIX}/include ${CPPFLAGS}"
export PATH="${LIBPQ_PREFIX}/bin:${PATH}"

# No need to set special C++ flags with charlock_holmes 0.7.9+

# Create a temporary file to store environment variables for the parent shell
ENV_FILE=$(mktemp)
cat > "${ENV_FILE}" << EOF
export PKG_CONFIG_PATH="${ICU4C_PREFIX}/lib/pkgconfig:${LIBPQ_PREFIX}/lib/pkgconfig:\${PKG_CONFIG_PATH}"
export LDFLAGS="-L${ICU4C_PREFIX}/lib -L${LIBPQ_PREFIX}/lib \${LDFLAGS}"
export CPPFLAGS="-I${ICU4C_PREFIX}/include -I${LIBPQ_PREFIX}/include \${CPPFLAGS}"
export PATH="${LIBPQ_PREFIX}/bin:\${PATH}"
EOF

echo ""
echo "✅ macOS dependencies setup complete!"
echo ""
echo "📝 Environment variables have been set for this session."
echo "   To make them permanent, add the following to your shell profile (~/.zshrc or ~/.bash_profile):"
echo ""
cat "${ENV_FILE}"
echo ""

# Pass the environment file path so the parent process can source it
echo "ENV_SETUP_FILE=${ENV_FILE}"
