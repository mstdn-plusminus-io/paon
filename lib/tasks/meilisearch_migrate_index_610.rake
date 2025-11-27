# frozen_string_literal: true

namespace :meilisearch do
  desc 'Migrate Meilisearch index settings for performance optimization (v6.1.0)'
  task 'migrate-index-610': :environment do
    unless Mastodon.meilisearch_enabled?
      puts 'Meilisearch is not enabled. Set MEILI_ENABLED=true in your environment.'
      exit 1
    end

    puts '=' * 60
    puts 'Meilisearch Index Migration (v6.1.0)'
    puts 'Performance optimization for Status index'
    puts '=' * 60
    puts ''

    # Get Meilisearch client
    client = MeiliSearch::Rails.client
    index_uid = "#{Mastodon.meilisearch_prefix}statuses"

    puts "Target index: #{index_uid}"
    puts ''

    # Check if index exists
    begin
      index = client.index(index_uid)
      stats = index.stats
      puts "Current index stats:"
      puts "  - Documents: #{stats['numberOfDocuments']}"
      puts "  - Index size: #{(stats['indexSize'] || 0) / 1024 / 1024} MB"
      puts ''
    rescue MeiliSearch::ApiError => e
      puts "Error: Index '#{index_uid}' not found or not accessible."
      puts "  #{e.message}"
      exit 1
    end

    # Get current settings
    puts 'Current settings:'
    current_settings = index.settings
    puts "  - filterableAttributes: #{current_settings['filterableAttributes']&.count || 0} attributes"
    puts "  - sortableAttributes: #{current_settings['sortableAttributes']&.count || 0} attributes"
    puts "  - typoTolerance enabled: #{current_settings.dig('typoTolerance', 'enabled')}"
    puts "  - proximityPrecision: #{current_settings['proximityPrecision']}"
    puts "  - facetSearch: #{current_settings['facetSearch']}"
    puts ''

    # Define new optimized settings
    new_settings = {
      'filterableAttributes' => %w[
        id
        account_id
        language
        visibility
        sensitive
        has_media
        has_image
        has_video
        has_poll
        has_link
        has_embed
        is_reply
        created_at_timestamp
      ],
      'sortableAttributes' => %w[
        created_at_timestamp
        favourites_count
        reblogs_count
      ],
      'typoTolerance' => {
        'enabled' => false
      },
      'proximityPrecision' => 'byAttribute',
      'facetSearch' => false
    }

    # Show changes
    puts 'Changes to be applied:'
    puts ''

    # filterableAttributes changes
    removed_filterable = (current_settings['filterableAttributes'] || []) - new_settings['filterableAttributes']
    if removed_filterable.any?
      puts '  filterableAttributes to REMOVE:'
      removed_filterable.each { |attr| puts "    - #{attr}" }
    end

    # sortableAttributes changes
    removed_sortable = (current_settings['sortableAttributes'] || []) - new_settings['sortableAttributes']
    if removed_sortable.any?
      puts '  sortableAttributes to REMOVE:'
      removed_sortable.each { |attr| puts "    - #{attr}" }
    end

    puts ''
    puts '  New settings:'
    puts "    - typoTolerance: { enabled: false }"
    puts "    - proximityPrecision: 'byAttribute'"
    puts "    - facetSearch: false"
    puts ''

    # Confirmation
    unless ENV['FORCE'] == 'true'
      puts '=' * 60
      puts 'WARNING: This will trigger a full reindex of all documents!'
      puts 'This may take several hours for large indexes.'
      puts ''
      puts 'To proceed, run with FORCE=true:'
      puts '  FORCE=true rake meilisearch:migrate-index-610'
      puts '=' * 60
      exit 0
    end

    # Apply settings
    puts 'Applying new settings...'
    start_time = Time.now

    begin
      task = index.update_settings(new_settings)
      puts "  Task UID: #{task['taskUid']}"
      puts '  Waiting for task to complete...'

      # Wait for the task to complete
      loop do
        task_status = client.task(task['taskUid'])
        status = task_status['status']

        case status
        when 'succeeded'
          puts "  Task completed successfully!"
          break
        when 'failed'
          puts "  Task failed!"
          puts "  Error: #{task_status['error']}"
          exit 1
        when 'canceled'
          puts "  Task was canceled!"
          exit 1
        else
          # enqueued, processing
          print '.'
          sleep 5
        end
      end
    rescue MeiliSearch::ApiError => e
      puts "  Error applying settings: #{e.message}"
      exit 1
    end

    elapsed = Time.now - start_time
    puts ''
    puts "Settings migration completed in #{elapsed.round(2)} seconds."
    puts ''

    # Show new settings
    puts 'New index settings:'
    new_current = index.settings
    puts "  - filterableAttributes: #{new_current['filterableAttributes']&.count || 0} attributes"
    puts "  - sortableAttributes: #{new_current['sortableAttributes']&.count || 0} attributes"
    puts "  - typoTolerance enabled: #{new_current.dig('typoTolerance', 'enabled')}"
    puts "  - proximityPrecision: #{new_current['proximityPrecision']}"
    puts "  - facetSearch: #{new_current['facetSearch']}"
    puts ''

    puts '=' * 60
    puts 'Migration completed!'
    puts ''
    puts 'IMPORTANT: You may need to reindex all documents to apply'
    puts 'the new settings to existing data. Run:'
    puts '  rake meilisearch:deploy'
    puts '=' * 60
  end
end
