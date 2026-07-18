# frozen_string_literal: true

class TagSearchService < BaseService
  def call(query, options = {})
    @query   = query.strip.delete_prefix('#')
    @offset  = options.delete(:offset).to_i
    @limit   = options.delete(:limit).to_i
    @options = options

    results   = from_meilisearch if Mastodon.meilisearch_enabled?
    results ||= from_database

    results
  end

  private

  def from_meilisearch
    search_options = {
      limit: @limit,
      offset: @offset,
      sort: ['usage:desc', 'accounts_count:desc']
    }

    # Add filter for reviewed tags if exclude_unreviewed is true
    if @options[:exclude_unreviewed]
      search_options[:filter] = 'reviewed = true'
    end

    results = Tag.search(@query, search_options)

    ensure_exact_match(results.to_a)
  rescue StandardError => e
    Rails.logger.error "Meilisearch error: #{e.message}"
    nil
  end

  # Since the ElasticSearch Query doesn't guarantee the exact match will be the
  # first result or that it will even be returned, patch the results accordingly
  def ensure_exact_match(results)
    return results unless @offset.nil? || @offset.zero?

    normalized_query = Tag.normalize(@query)
    exact_match = results.find { |tag| tag.name.downcase == normalized_query }
    exact_match ||= Tag.find_normalized(normalized_query)
    unless exact_match.nil?
      results.delete(exact_match)
      results = [exact_match] + results
    end

    results
  end

  def from_database
    Tag.search_for(@query, @limit, @offset, @options)
  end
end
