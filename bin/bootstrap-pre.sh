#!/bin/bash

if [[ "$(uname)" == "Darwin" ]]; then
  echo "🍎 Detected macOS, temporally kill VSCode internal RipGrep due to fucking issue https://github.com/microsoft/vscode/issues/186279 ..."
  chmod a-x '/Applications/Visual Studio Code.app/Contents/Resources/app/node_modules/@vscode/ripgrep/bin/rg'
fi
