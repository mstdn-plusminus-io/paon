FROM mcr.microsoft.com/devcontainers/base:trixie

RUN apt-get update \
    && export DEBIAN_FRONTEND=noninteractive \
    && apt-get install -y --no-install-recommends ffmpeg libvips-dev pamtester pkg-config \
    && rm -rf /var/lib/apt/lists/*

COPY welcome-message.txt /usr/local/etc/vscode-dev-containers/first-run-notice.txt
