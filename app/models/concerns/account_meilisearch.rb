# frozen_string_literal: true

module AccountMeilisearch
  extend ActiveSupport::Concern

  included do
    if Mastodon.meilisearch_enabled?
      include MeiliSearch::Rails

      meilisearch index_uid: "#{Mastodon.meilisearch_prefix}accounts", primary_key: :id, if: :searchable? do
        attribute :id, :username, :display_name, :domain, :bot, :locked, :discoverable, :indexable

        attribute :text do
          ::PlainTextFormatter.new(note, local?).to_s if discoverable?
        end

        attribute :followers_count do
          account_stat&.followers_count || 0
        end

        attribute :following_count do
          account_stat&.following_count || 0
        end

        attribute :statuses_count do
          account_stat&.statuses_count || 0
        end

        attribute :last_status_at do
          account_stat&.last_status_at&.to_i || 0
        end

        attribute :created_at_timestamp do
          created_at.to_i
        end

        searchable_attributes [:username, :display_name, :text]

        ranking_rules [
          'words',
          'typo',
          'proximity',
          'attribute',
          'sort',
          'followers_count:desc',
          'statuses_count:desc',
          'last_status_at:desc'
        ]

        filterable_attributes [:domain, :bot, :locked, :discoverable, :indexable]
        sortable_attributes [:followers_count, :following_count, :statuses_count, :last_status_at, :created_at_timestamp]
      end
    end
  end

  def searchable?
    !suspended? && !moved? && discoverable?
  end

  private

  def suspended?
    suspended_at.present?
  end

  def moved?
    moved_to_account_id.present?
  end
end
