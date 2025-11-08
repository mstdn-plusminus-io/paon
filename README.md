![](./lib/assets/logo_full.png)

[![GitHub release](https://img.shields.io/github/release/mstdn-plusminus-io/paon.svg)][releases]
[![build latest image](https://github.com/mstdn-plusminus-io/paon/actions/workflows/latest.yml/badge.svg?branch=master)](https://github.com/mstdn-plusminus-io/paon/actions/workflows/latest.yml)
[![build staging image](https://github.com/mstdn-plusminus-io/paon/actions/workflows/staging.yml/badge.svg?branch=staging)](https://github.com/mstdn-plusminus-io/paon/actions/workflows/staging.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/plusminusio/paon.svg)][docker]

[releases]: https://github.com/mstdn-plusminus-io/paon/releases
[docker]: https://hub.docker.com/r/plusminusio/paon/

Paon -ぱおん- is a fork of Mastodon. It aims to maintain the look and feel of Mastodon v4.2.x while adding features from 4.3.x onwards and unique features, and to provide security updates.  
Since the database schema remains unchanged, it can be used as a drop-in replacement for 4.2.x.

## Updated

- Rails 7.0.x -> 7.2.x
- Webpacker + webpack -> Shakapacker + rspack
- Some gems

## Additional and/or changed features

### Toot

- Toot length limit is increase to 5000 characters
- Support quote (compatible; Mastodon 4.4.x, Misskey, maybe Fedibird)

### User interfaces

- Add theme of Slack like user interfaces
- Spoiler message preset
- Side navigation in right side or left side on phone
- Show relative time or absolute time in toot timeline
- Toot button position on phone
- Plain text or render GitHub Flavored Markdown (experimental)
- Preview search box by Misskey Flavored Markdown
- Show original post link in toot timeline
- ... and more!

### Server

- Configurable use [Cloudflare Turnstile](https://www.cloudflare.com/ja-jp/products/turnstile/) at signup
  - `CLOUDFLARE_TURNSTILE_ENABLED=true`
  - `CLOUDFLARE_TURNSTILE_SITE_KEY=1x00000000000000000000AA`
  - `CLOUDFLARE_TURNSTILE_SECRET_KEY=1x0000000000000000000000000000000AA`
- Configurable enable or disable signup by REST API
  - `DISABLE_SIGNUP_BY_API=true`
- Configurable enable or disable remote media cache like Pleroma
  - `DISABLE_REMOTE_MEDIA_CACHE=true`

## Start develop

Before developing, you need to install the following software.

- Ruby 3.2.x
- Node.js 22.x
- Yarn 1.22.x

Then run the following commands.

```sh
yarn docker:dev up -d
bundle install
cp .env.sample .env
rails db:migrate
yarn watch
```

## License

```
Copyright (C) Paon contributors  
Copyright (C) 2016-2023 Eugen Rochko & other Mastodon and contributors (see [AUTHORS.md](AUTHORS.md))

This program is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License along with this program. If not, see <https://www.gnu.org/licenses/>.
```
