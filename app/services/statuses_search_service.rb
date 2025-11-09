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
    search_options = {
      limit: @limit,
      offset: @offset,
      sort: ['created_at_timestamp:desc']
    }

    filters = build_filters

    search_options[:filter] = filters.join(' AND ') if filters.any?

    results = Status.search(@query, search_options)
    results = results.to_a

    account_ids         = results.map(&:account_id)
    account_domains     = results.map(&:account_domain)
    preloaded_relations = @account.relations_map(account_ids, account_domains)

    results.reject { |status| StatusFilter.new(status, @account, preloaded_relations).filtered? }
  rescue StandardError => e
    Rails.logger.error "Meilisearch error: #{e.message}"
    []
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
    # For now, only search public and unlisted statuses
    # TODO: Implement proper searchable_by filtering for private statuses
    filters << 'visibility IN [public, unlisted]'

    filters
  end
end
