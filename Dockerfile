FROM ruby:3.2-slim-bullseye

ARG MASTODON_VERSION_METADATA=""

ENV DEBIAN_FRONTEND=noninteractive

# Install Node.js
RUN \
  set -xe && \
  apt-get update && \
  apt-get install -y --no-install-recommends curl && \
  curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
  apt-get install -y nodejs && \
  rm -rf /share/doc && \
  apt-get -y --auto-remove purge curl && \
  apt-get clean && \
  rm -rf /var/lib/apt-get/lists/*


# Enable jemalloc
RUN \
  set -xe && \
  apt-get update && \
  apt-get -y --no-install-recommends install libjemalloc2 && \
  ln -nfs /usr/lib/$(uname -m)-linux-gnu /usr/lib/linux-gnu && \
  apt-get clean && \
  rm -rf /var/lib/apt-get/lists/*
ENV LD_PRELOAD=${LD_PRELOAD}:/usr/lib/linux-gnu/libjemalloc.so.2

# Create the mastodon user
ARG UID=991
ARG GID=991
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN \
  set -xe && \
  apt-get update && \
  echo "Etc/UTC" > /etc/localtime && \
  apt-get -y --no-install-recommends install whois wget && \
  addgroup --gid $GID mastodon && \
  useradd -m -u $UID -g $GID -d /opt/mastodon mastodon && \
  echo "mastodon:$(head /dev/urandom | tr -dc A-Za-z0-9 | head -c 24 | mkpasswd -s -m sha-256)" | chpasswd && \
  apt-get clean && \
  rm -rf /var/lib/apt-get/lists/*

COPY --chown=mastodon:mastodon . /opt/mastodon

# Run mastodon services in prod mode
ENV RAILS_ENV="production"
ENV NODE_ENV="production"

# Tell rails to serve static files
ENV RAILS_SERVE_STATIC_FILES="true"
ENV BIND="0.0.0.0"
ENV MASTODON_VERSION_METADATA="${MASTODON_VERSION_METADATA}"

ENV NODE_OPTIONS="--openssl-legacy-provider"

# Build mastodon, and set permissions
RUN \
  set -xe && \
  apt-get update && \
  apt-get -y --no-install-recommends install apt-utils build-essential git libicu-dev libidn11-dev libpq-dev libprotobuf-dev protobuf-compiler shared-mime-info libssl-dev libssl1.1 libpq5 imagemagick ffmpeg libyaml-0-2 file ca-certificates tzdata libreadline8 gcc tini && \
  ln -s /opt/mastodon /mastodon && \
  gem install bundler && \
  npm install -g npm@latest && \
  npm install -g yarn && \
  yarn config set network-timeout 600000 && \
  npm install -g node-gyp && \
  cd /opt/mastodon && \
  bundle config set --local deployment 'true' && \
  bundle config set --local without 'development test' && \
  bundle config set silence_root_warning true && \
  bundle install -j"$(nproc)" && \
  yarn install --pure-lockfile && \
  rm -rf /opt/mastodon/public/packs /opt/mastodon/public/packs-test && \
  OTP_SECRET=precompile_placeholder SECRET_KEY_BASE=precompile_placeholder bundle exec rails assets:precompile && \
  rm -rf /opt/mastodon/node_modules/.cache && \
  chown -R mastodon:mastodon /opt/mastodon && \
  npm cache clean --force && \
  yarn cache clean && \
  apt-get -y --auto-remove purge git libicu-dev libidn11-dev libpq-dev libprotobuf-dev protobuf-compiler gcc build-essential && \
  apt-get -y --no-install-recommends install libssl1.1 libpq5 imagemagick ffmpeg libjemalloc2 libicu67 libprotobuf23 libidn11 libyaml-0-2 ca-certificates tzdata libreadline8 && \
  apt-get clean && \
  rm -rf /var/lib/apt-get/lists/*

# Set the run user
USER mastodon

# Set the work dir and the container entry point
WORKDIR /opt/mastodon
ENTRYPOINT ["/usr/bin/tini", "--"]
EXPOSE 3000 4000
