# frozen_string_literal: true

host            = ENV.fetch('MEILI_HOST') { 'http://localhost:7700' }
api_key         = ENV.fetch('MEILI_MASTER_KEY') { nil }

MeiliSearch::Rails.configuration = {
  meilisearch_url: host,
  meilisearch_api_key: api_key,
  pagination_backend: :kaminari,
}

# Meilisearchが無効の場合、検索機能を無効化する定数を設定
# 既存のMastodonモジュールに定数を追加
unless Mastodon.const_defined?(:MEILISEARCH_ENABLED)
  Mastodon.const_set(:MEILISEARCH_ENABLED, ENV['MEILI_ENABLED'] == 'true')
end

unless Mastodon.const_defined?(:MEILISEARCH_PREFIX)
  Mastodon.const_set(:MEILISEARCH_PREFIX, begin
    fallback_prefix = ENV.fetch('REDIS_NAMESPACE', nil).presence
    prefix = ENV.fetch('MEILI_PREFIX') { fallback_prefix }.presence
    prefix ? "#{prefix}_" : ''
  end)
end

# トップレベルの定数も定義（後方互換性のため）
unless defined?(::MEILISEARCH_ENABLED)
  ::MEILISEARCH_ENABLED = Mastodon::MEILISEARCH_ENABLED
end

unless defined?(::MEILISEARCH_PREFIX)
  ::MEILISEARCH_PREFIX = Mastodon::MEILISEARCH_PREFIX
end
