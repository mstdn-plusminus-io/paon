# frozen_string_literal: true

class StatusesSearchService < BaseService
  def call(query, account = nil, options = {})
    @query   = query&.strip
    @account = account
    @options = options
    @limit   = options[:limit].to_i
    @offset  = options[:offset].to_i

    return [] if @query.blank?

    status_search_results
  end

  private

  def status_search_results
    # Try to parse the query using the search query parser
    parsed_query = parse_query(@query)

    if parsed_query
      # Use the parsed query with filters
      search_with_filters(parsed_query)
    else
      # Fall back to simple text search if parsing fails
      simple_search(@query)
    end
  end

  def parse_query(query)
    parser = SearchQueryParser.new
    transformer = MeilisearchQueryTransformer.new
    transformer.current_account = @account

    begin
      tree = parser.parse(query)
      transformer.apply(tree, current_account: @account)
    rescue Parslet::ParseFailed
      # Return nil if parsing fails
      nil
    end
  end

  def search_with_filters(parsed_query)
    # Handle bookmark mode separately
    if parsed_query.bookmark_mode?
      return search_bookmarks(parsed_query)
    end

    meilisearch_params = parsed_query.to_meilisearch_query
    postgresql_conditions = parsed_query.to_postgresql_conditions

    # Determine limit multiplier based on PostgreSQL filters
    # If we have post-filters, fetch more results to account for filtering
    limit_multiplier = postgresql_conditions.any? ? 3 : 1

    search_options = {
      limit: @limit * limit_multiplier,
      offset: @offset
    }

    # Add parsed filters
    search_options[:filter] = meilisearch_params[:filter] if meilisearch_params[:filter].present?
    search_options[:sort] = meilisearch_params[:sort] if meilisearch_params[:sort].present?

    # Add additional filters from options
    additional_filters = build_additional_filters
    if additional_filters.any?
      if search_options[:filter].present?
        search_options[:filter] = "#{search_options[:filter]} AND #{additional_filters.join(' AND ')}"
      else
        search_options[:filter] = additional_filters.join(' AND ')
      end
    end

    # Use the parsed query string or empty string if no text terms
    query_string = meilisearch_params[:query].presence || ''

    results = Status.search(query_string, search_options)
    status_ids = results.map(&:id)

    # Apply PostgreSQL post-filtering if needed
    if postgresql_conditions.any? && status_ids.any?
      statuses = apply_postgresql_filters(status_ids, postgresql_conditions)
    else
      statuses = Status.where(id: status_ids).index_by(&:id)
      statuses = status_ids.filter_map { |id| statuses[id] }
    end

    filter_results(statuses.first(@limit))
  rescue StandardError => e
    Rails.logger.error "Meilisearch search_with_filters error: #{e.message}"
    Rails.logger.error e.backtrace.join("\n")
    []
  end

  def search_bookmarks(parsed_query)
    meilisearch_params = parsed_query.to_meilisearch_query
    postgresql_conditions = parsed_query.to_postgresql_conditions

    # Get bookmarked status IDs
    bookmark_status_ids = Bookmark.where(account: @account).pluck(:status_id)
    return [] if bookmark_status_ids.empty?

    # Limit multiplier for PostgreSQL post-filtering
    limit_multiplier = postgresql_conditions.any? ? 3 : 1

    search_options = {
      limit: @limit * limit_multiplier,
      offset: @offset,
      filter: "id IN [#{bookmark_status_ids.join(',')}]"
    }

    # Add other filters from parsed query
    if meilisearch_params[:filter].present?
      search_options[:filter] = "#{search_options[:filter]} AND #{meilisearch_params[:filter]}"
    end
    search_options[:sort] = meilisearch_params[:sort] if meilisearch_params[:sort].present?

    query_string = meilisearch_params[:query].presence || ''

    results = Status.search(query_string, search_options)
    status_ids = results.map(&:id)

    # Apply PostgreSQL post-filtering if needed
    if postgresql_conditions.any? && status_ids.any?
      statuses = apply_postgresql_filters(status_ids, postgresql_conditions)
    else
      statuses = Status.where(id: status_ids).index_by(&:id)
      statuses = status_ids.filter_map { |id| statuses[id] }
    end

    filter_results(statuses.first(@limit))
  rescue StandardError => e
    Rails.logger.error "Meilisearch search_bookmarks error: #{e.message}"
    Rails.logger.error e.backtrace.join("\n")
    []
  end

  def simple_search(query)
    search_options = {
      limit: @limit,
      offset: @offset,
      sort: ['created_at_timestamp:desc']
    }

    filters = build_filters
    search_options[:filter] = filters.join(' AND ') if filters.any?

    results = Status.search(query, search_options)
    status_ids = results.map(&:id)

    # Preserve order from Meilisearch
    statuses = Status.where(id: status_ids).index_by(&:id)
    statuses = status_ids.filter_map { |id| statuses[id] }

    filter_results(statuses)
  rescue StandardError => e
    Rails.logger.error "Meilisearch simple_search error: #{e.message}"
    []
  end

  def filter_results(results)
    account_ids         = results.map(&:account_id)
    account_domains     = results.map(&:account_domain)
    preloaded_relations = @account.relations_map(account_ids, account_domains)

    results.reject { |status| StatusFilter.new(status, @account, preloaded_relations).filtered? }
  end

  def build_additional_filters
    filters = []

    # Filter by account_id if specified in options
    if @options[:account_id]
      filters << "account_id = #{@options[:account_id]}"
    end

    # Filter by time range if specified
    if @options[:min_id]
      timestamp = Mastodon::Snowflake.to_time(@options[:min_id].to_i).to_i
      filters << "created_at_timestamp >= #{timestamp}"
    end

    if @options[:max_id]
      timestamp = Mastodon::Snowflake.to_time(@options[:max_id].to_i).to_i
      filters << "created_at_timestamp <= #{timestamp}"
    end

    filters
  end

  def build_filters
    filters = []

    # Filter by account_id if specified
    if @options[:account_id]
      filters << "account_id = #{@options[:account_id]}"
    end

    # Filter by time range if specified
    if @options[:min_id]
      timestamp = Mastodon::Snowflake.to_time(@options[:min_id].to_i).to_i
      filters << "created_at_timestamp >= #{timestamp}"
    end

    if @options[:max_id]
      timestamp = Mastodon::Snowflake.to_time(@options[:max_id].to_i).to_i
      filters << "created_at_timestamp <= #{timestamp}"
    end

    # Default visibility filter: public posts OR user's own posts
    if @account
      filters << "(visibility = \"public\" OR account_id = #{@account.id})"
    else
      filters << 'visibility = "public"'
    end

    filters
  end

  # Apply PostgreSQL filters for has:*, is:*, language: queries
  def apply_postgresql_filters(status_ids, conditions)
    scope = Status.where(id: status_ids)

    conditions.each do |condition|
      scope = apply_single_condition(scope, condition)
    end

    # Preserve order from Meilisearch
    statuses_by_id = scope.index_by(&:id)
    status_ids.filter_map { |id| statuses_by_id[id] }
  end

  def apply_single_condition(scope, condition)
    case condition[:type]
    when :language
      if condition[:negated]
        scope.where.not(language: condition[:value])
      else
        scope.where(language: condition[:value])
      end
    when :has_media
      if condition[:negated]
        scope.left_joins(:media_attachments).where(media_attachments: { id: nil })
      else
        scope.joins(:media_attachments).distinct
      end
    when :has_image
      if condition[:negated]
        scope.left_joins(:media_attachments).where.not(media_attachments: { type: 'image' })
      else
        scope.joins(:media_attachments).where(media_attachments: { type: 'image' }).distinct
      end
    when :has_video
      if condition[:negated]
        scope.left_joins(:media_attachments).where.not(media_attachments: { type: %w[video gifv] })
      else
        scope.joins(:media_attachments).where(media_attachments: { type: %w[video gifv] }).distinct
      end
    when :has_poll
      if condition[:negated]
        scope.left_joins(:preloadable_poll).where(polls: { id: nil })
      else
        scope.joins(:preloadable_poll)
      end
    when :has_link
      if condition[:negated]
        scope.left_joins(:preview_cards).where(preview_cards: { id: nil })
      else
        scope.joins(:preview_cards).distinct
      end
    when :has_embed
      if condition[:negated]
        scope.left_joins(:preview_cards).where.not(preview_cards: { type: 'video' }).where(preview_cards: { html: [nil, ''] })
      else
        scope.joins(:preview_cards).where(preview_cards: { type: 'video' }).or(
          scope.joins(:preview_cards).where.not(preview_cards: { html: [nil, ''] })
        ).distinct
      end
    when :sensitive
      if condition[:negated]
        scope.where(sensitive: false)
      else
        scope.where(sensitive: true)
      end
    when :is_reply
      if condition[:negated]
        scope.where(in_reply_to_id: nil)
      else
        scope.where.not(in_reply_to_id: nil)
      end
    else
      scope
    end
  end
end
