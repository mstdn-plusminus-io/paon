# frozen_string_literal: true

namespace :meilisearch do
  desc 'Create or update Meilisearch indices and populate them with data'
  task deploy: :environment do
    unless Mastodon.meilisearch_enabled?
      puts 'Meilisearch is not enabled. Set MEILI_ENABLED=true in your environment.'
      exit 1
    end

    require 'ruby-progressbar'

    # Get batch size from environment variable or use default
    batch_size = (ENV['BATCH_SIZE'] || 100).to_i
    puts "Batch size: #{batch_size}"

    models = [
      { name: 'Account', model: Account },
      { name: 'Status', model: Status },
      { name: 'Tag', model: Tag },
      { name: 'Instance', model: Instance },
    ]

    puts 'Starting Meilisearch index deployment...'
    puts ''

    models.each do |model_info|
      model_name = model_info[:name]
      model_class = model_info[:model]

      puts "Reindexing #{model_name}..."
      start_time = Time.now

      begin
        puts "  → Counting records..."
        count_start = Time.now
        total_count = model_class.count
        count_elapsed = Time.now - count_start
        puts "  → Found #{total_count} records (took #{count_elapsed.round(2)}s)"

        indexed_count = 0

        progress = ProgressBar.create(
          title: "  #{model_name}",
          total: total_count,
          format: '%t %c/%C |%B| %p%% %e',
          output: $stdout
        )

        puts "  → Starting batch indexing..."

        # Manually batch index with progress updates
        model_class.find_in_batches(batch_size: batch_size) do |batch|
          # Filter records that should be indexed
          indexable_records = batch.select { |record| record.respond_to?(:should_index?) ? record.should_index? : true }

          if indexable_records.any?
            # Add documents to index
            model_class.index_documents(indexable_records)
          end

          indexed_count += batch.size
          # Prevent progress from exceeding total
          progress.progress = [indexed_count, total_count].min
        end

        progress.finish

        elapsed = Time.now - start_time
        puts "  ✓ Completed in #{elapsed.round(2)} seconds (#{indexed_count} records)"
      rescue StandardError => e
        puts "  ✗ Error: #{e.message}"
        puts e.backtrace.first(5).map { |line| "    #{line}" }.join("\n")
      end

      puts ''
    end

    puts 'Meilisearch index deployment completed!'
  end

  desc 'Clear all Meilisearch indices'
  task clear: :environment do
    unless Mastodon.meilisearch_enabled?
      puts 'Meilisearch is not enabled. Set MEILI_ENABLED=true in your environment.'
      exit 1
    end

    models = [Account, Status, Tag, Instance]

    puts 'Clearing Meilisearch indices...'
    puts ''

    models.each do |model_class|
      model_name = model_class.name

      puts "Clearing #{model_name} index..."
      begin
        model_class.clear_index!
        puts "  ✓ Cleared"
      rescue StandardError => e
        puts "  ✗ Error: #{e.message}"
      end
    end

    puts ''
    puts 'All Meilisearch indices cleared!'
  end
end
