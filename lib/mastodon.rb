# frozen_string_literal: true

# Mastodonモジュールの基本定義
# このファイルですべてのMastodonモジュールレベルのメソッドを定義
module Mastodon
  class << self
    def meilisearch_enabled?
      if const_defined?(:MEILISEARCH_ENABLED, false)
        const_get(:MEILISEARCH_ENABLED)
      else
        ENV['MEILI_ENABLED'] == 'true'
      end
    end

    def meilisearch_prefix
      if const_defined?(:MEILISEARCH_PREFIX, false)
        const_get(:MEILISEARCH_PREFIX)
      else
        fallback = ENV.fetch('REDIS_NAMESPACE', nil)&.presence
        p = ENV.fetch('MEILI_PREFIX') { fallback }&.presence
        p ? "#{p}_" : ''
      end
    end
  end
end