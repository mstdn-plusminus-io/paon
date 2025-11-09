# frozen_string_literal: true

module StatusMeilisearch
  extend ActiveSupport::Concern

  included do
    if Mastodon.meilisearch_enabled?
      include MeiliSearch::Rails

      meilisearch index_uid: "#{Mastodon.meilisearch_prefix}statuses", primary_key: :id, if: :searchable? do
        attribute :id, :account_id, :in_reply_to_id, :reblog_of_id, :language, :sensitive

        attribute :text do
          searchable_text
        end

        attribute :tags do
          tags.map(&:name)
        end

        attribute :visibility

        attribute :searchable_by do
          searchable_by_ids
        end

        attribute :has_media do
          media_attachments.any?
        end

        attribute :has_poll do
          preloadable_poll.present?
        end

        attribute :has_link do
          preview_cards.any?
        end

        attribute :created_at_timestamp do
          created_at.to_i
        end

        attribute :favourites_count
        attribute :reblogs_count
        attribute :replies_count

        searchable_attributes [:text, :tags]

        ranking_rules [
          'words',
          'typo',
          'proximity',
          'attribute',
          'sort',
          'created_at_timestamp:desc',
          'favourites_count:desc',
          'reblogs_count:desc'
        ]

        filterable_attributes [:account_id, :language, :visibility, :sensitive, :has_media, :has_poll, :has_link, :searchable_by]
        sortable_attributes [:created_at_timestamp, :favourites_count, :reblogs_count, :replies_count]
      end
    end
  end

  def searchable?
    public_visibility? || unlisted_visibility?
  end

  def searchable_text
    return @searchable_text if defined?(@searchable_text)

    @searchable_text = [
      ::PlainTextFormatter.new(text, local?).to_s,
      spoiler_text,
      preloadable_poll&.options&.join(' ')
    ].compact.join("\n\n")
  end

  def searchable_by_ids
    case visibility.to_sym
    when :public, :unlisted
      []
    when :private
      account.followers.pluck(:id) + [account_id]
    when :direct
      mentions.pluck(:account_id) + [account_id]
    else
      []
    end
  end
end
