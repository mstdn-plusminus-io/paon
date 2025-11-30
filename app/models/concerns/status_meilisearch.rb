# frozen_string_literal: true

module StatusMeilisearch
  extend ActiveSupport::Concern

  included do
    if Mastodon.meilisearch_enabled?
      include MeiliSearch::Rails

      meilisearch index_uid: "#{Mastodon.meilisearch_prefix}statuses", primary_key: :id, if: :searchable? do
        attribute :id, :account_id, :reblog_of_id, :language, :sensitive

        attribute :text do
          searchable_text
        end

        attribute :tags do
          tags.map(&:name)
        end

        attribute :visibility

        attribute :has_media do
          media_attachments.any?
        end

        attribute :has_image do
          media_attachments.any? { |m| m.type == 'image' }
        end

        attribute :has_video do
          media_attachments.any? { |m| m.type == 'video' || m.type == 'gifv' }
        end

        attribute :has_poll do
          preloadable_poll.present?
        end

        attribute :has_link do
          preview_cards.any?
        end

        attribute :has_embed do
          preview_cards.any? { |card| card.type == 'video' || card.html.present? }
        end

        attribute :is_reply do
          in_reply_to_id.present?
        end

        attribute :created_at_timestamp do
          created_at.to_i
        end

        attribute :favourites_count
        attribute :reblogs_count

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

        # Minimal filterable attributes for performance optimization
        # - id: required for in:bookmark filter
        # - account_id: required for from:, in:library filters
        # - visibility: required for visibility-based filtering
        # - created_at_timestamp: required for before:, after:, during: filters
        # Other filters (has:*, is:*, language:) are handled by PostgreSQL post-filtering
        filterable_attributes [:id, :account_id, :visibility, :created_at_timestamp]
        sortable_attributes [:created_at_timestamp]

        # Performance optimizations
        typo_tolerance enabled: false
        proximity_precision 'byAttribute'
      end
    end
  end

  def searchable?
    if Mastodon.meilisearch_library_only?
      # MEILI_LIBRARY_ONLY=true: Index local statuses OR remote statuses with local interactions
      local? || has_local_interaction?
    else
      # Default: Index all local statuses + remote public statuses
      local? || public_visibility?
    end
  end

  def has_local_interaction?
    return false if local?

    # Check if any local user has interacted with this remote status
    local_favorited.exists? ||
      local_reblogged.exists? ||
      local_bookmarked.exists? ||
      local_mentioned.exists?
  end

  def searchable_text
    return @searchable_text if defined?(@searchable_text)

    @searchable_text = [
      ::PlainTextFormatter.new(text, local?).to_s,
      spoiler_text,
      preloadable_poll&.options&.join(' ')
    ].compact.join("\n\n")
  end
end
