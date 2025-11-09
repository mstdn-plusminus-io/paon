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
    meilisearch_params = parsed_query.to_meilisearch_query

    search_options = {
      limit: @limit,
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
    filter_results(results.to_a)
  rescue StandardError => e
    Rails.logger.error "Meilisearch search_with_filters error: #{e.message}"
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
    filter_results(results.to_a)
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

    # Filter by searchable_by (visibility control)
    # Default: only public and unlisted, or posts the user can see
    # Note: searchable_by filtering is more complex and may not work as expected with Meilisearch
    # For now, simplify to just visibility filtering
    filters << "(visibility = \"public\" OR visibility = \"unlisted\")"

    filters
  end
end
