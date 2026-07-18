FROM mcr.microsoft.com/devcontainers/go:1-1.25-bookworm

RUN apt-get update \
    && export DEBIAN_FRONTEND=noninteractive \
    && apt-get install -y --no-install-recommends ffmpeg pamtester \
    && rm -rf /var/lib/apt/lists/*

COPY welcome-message.txt /usr/local/etc/vscode-dev-containers/first-run-notice.txt
