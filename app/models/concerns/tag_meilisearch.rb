# frozen_string_literal: true

module TagMeilisearch
  extend ActiveSupport::Concern

  included do
    if Mastodon.meilisearch_enabled?
      include MeiliSearch::Rails

      meilisearch index_uid: "#{Mastodon.meilisearch_prefix}tags", primary_key: :id, if: :listable? do
        attribute :id, :name, :trendable

        attribute :reviewed do
          reviewed?
        end

        attribute :usage do
          time_period = 7.days.ago.to_date..0.days.ago.to_date
          history.aggregate(time_period).uses
        end

        attribute :accounts_count do
          time_period = 7.days.ago.to_date..0.days.ago.to_date
          history.aggregate(time_period).accounts
        end

        attribute :last_status_at do
          last_status_at&.to_i || 0
        end

        searchable_attributes [:name]

        ranking_rules [
          'words',
          'typo',
          'proximity',
          'sort',
          'usage:desc',
          'accounts_count:desc',
          'last_status_at:desc'
        ]

        filterable_attributes [:reviewed, :trendable]
        sortable_attributes [:usage, :accounts_count, :last_status_at]
      end
    end
  end
end
