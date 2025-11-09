# frozen_string_literal: true

class Vacuum::StatusesVacuum
  include Redisable

  def initialize(retention_period)
    @retention_period = retention_period
  end

  def perform
    vacuum_statuses! if @retention_period.present?
  end

  private

  def vacuum_statuses!
    statuses_scope.in_batches do |statuses|
      # Side-effects not covered by foreign keys, such
      # as the search index, must be handled first.
      statuses.direct_visibility
              .includes(mentions: :account)
              .find_each(&:unlink_from_conversations!)

      # Note: With Meilisearch, deleted records should be automatically removed
      # from the index. The old Chewy/Elasticsearch code has been removed.

      # Foreign keys take care of most associated records for us.
      # Media attachments will be orphaned.
      statuses.delete_all
    end
  end

  def statuses_scope
    Status.unscoped.kept
          .joins(:account).merge(Account.remote)
          .where('statuses.id < ?', retention_period_as_id)
  end

  def retention_period_as_id
    Mastodon::Snowflake.id_at(@retention_period.ago, with_random: false)
  end
end
