# frozen_string_literal: true

# Mastodonモジュールのメソッドを確実に定義する
# Railsのコードリロード時にもメソッドが保持されるようにする

Rails.application.reloader.to_prepare do
  # lib/mastodonを再度ロードして、メソッドが確実に定義されるようにする
  require_relative '../../lib/mastodon'
end