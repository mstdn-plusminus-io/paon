# frozen_string_literal: true

class Scheduler::InstanceRefreshScheduler
  include Sidekiq::Worker

  sidekiq_options retry: 0, lock: :until_executed, lock_ttl: 1.day.to_i

  def perform
    # メソッドが存在しない場合は、lib/mastodonを再度ロード
    unless Mastodon.respond_to?(:meilisearch_enabled?)
      require_relative '../../../lib/mastodon'
    end

    Instance.refresh

    if Mastodon.meilisearch_enabled?
      Instance.reindex!
    end
  end
end
