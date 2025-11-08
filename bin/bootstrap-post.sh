#!/bin/bash

if [[ "$(uname)" == "Darwin" ]]; then
  echo "🍎 Detected macOS, restoring VSCode internal RipGrep permissions..."
  chmod a+x '/Applications/Visual Studio Code.app/Contents/Resources/app/node_modules/@vscode/ripgrep/bin/rg'
fi
