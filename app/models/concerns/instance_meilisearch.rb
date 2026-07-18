# frozen_string_literal: true

module InstanceMeilisearch
  extend ActiveSupport::Concern

  included do
    if Mastodon.meilisearch_enabled?
      include MeiliSearch::Rails

      meilisearch index_uid: "#{Mastodon.meilisearch_prefix}instances", primary_key: :id, if: :searchable? do
        attribute :id, :domain

        attribute :accounts_count do
          accounts.count
        end

        searchable_attributes [:domain]

        ranking_rules [
          'words',
          'typo',
          'proximity',
          'sort',
          'accounts_count:desc'
        ]

        sortable_attributes [:accounts_count]
      end
    end
  end

  def searchable?
    domain.present?
  end
end
