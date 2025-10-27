#!/bin/bash

# Bootstrap script that sets up dependencies and runs yarn bootstrap
set -e

echo "🚀 Starting Mastodon bootstrap process..."

# Run macOS dependencies setup if on Darwin
if [[ "$(uname)" == "Darwin" ]]; then
    echo "🍎 Detected macOS, setting up dependencies..."

    # Run the setup script and capture the output
    SETUP_OUTPUT=$(./bin/setup-mac-deps.sh)
    echo "${SETUP_OUTPUT}"

    # Extract the environment file path and source it
    ENV_FILE=$(echo "${SETUP_OUTPUT}" | grep "ENV_SETUP_FILE=" | cut -d'=' -f2)
    if [[ -n "${ENV_FILE}" ]] && [[ -f "${ENV_FILE}" ]]; then
        source "${ENV_FILE}"
        rm -f "${ENV_FILE}"
    fi
fi

echo ""
echo "📦 Running yarn to install Node.js dependencies..."
yarn --pure-lockfile

echo ""
echo "💎 Installing Ruby dependencies..."
gem install foreman

echo "📦 Installing Ruby gems..."
bundle install

echo ""
echo "✅ Bootstrap complete! You can now run 'yarn watch' to start the development server."
