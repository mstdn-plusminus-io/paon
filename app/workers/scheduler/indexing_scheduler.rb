# frozen_string_literal: true

class Scheduler::IndexingScheduler
  include Sidekiq::Worker
  include Redisable
  include DatabaseHelper

  sidekiq_options retry: 0, lock: :until_executed, lock_ttl: 30.minutes.to_i

  IMPORT_BATCH_SIZE = 1000
  SCAN_BATCH_SIZE = 10 * IMPORT_BATCH_SIZE

  def perform
    # メソッドが存在しない場合は、lib/mastodonを再度ロード
    unless Mastodon.respond_to?(:meilisearch_enabled?)
      require_relative '../../../lib/mastodon'
    end

    return unless Mastodon.meilisearch_enabled?

    models.each do |model_info|
      model_class = model_info[:model]
      index_name = model_info[:index]

      with_redis do |redis|
        redis.sscan_each("chewy:queue:#{index_name}", count: SCAN_BATCH_SIZE).each_slice(IMPORT_BATCH_SIZE) do |ids|
          # Meilisearch-rails automatically updates the index when records are saved/updated
          # Trigger a touch to update the index through ActiveRecord callbacks
          model_class.where(id: ids).find_each do |record|
            record.ms_index! if record.respond_to?(:ms_index!)
          end

          redis.srem("chewy:queue:#{index_name}", ids)
        end
      end
    end
  end

  private

  def models
    [
      { model: Account, index: 'AccountsIndex' },
      { model: Tag, index: 'TagsIndex' },
      { model: Status, index: 'PublicStatusesIndex' },
      { model: Status, index: 'StatusesIndex' },
    ]
  end
end
