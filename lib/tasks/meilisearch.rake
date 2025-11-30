# frozen_string_literal: true

namespace :meilisearch do
  desc 'Create or update Meilisearch indices and populate them with data'
  task deploy: :environment do
    unless Mastodon.meilisearch_enabled?
      puts 'Meilisearch is not enabled. Set MEILI_ENABLED=true in your environment.'
      exit 1
    end

    require 'ruby-progressbar'
    require 'json'
    require 'fileutils'

    # Progress file path
    progress_file = Rails.root.join('tmp', 'meilisearch_deploy_progress.json')
    interrupted = false

    # Signal handler for graceful interruption
    Signal.trap('INT') do
      interrupted = true
      puts "\n\n⚠ Interrupt received. Saving progress..."
    end

    # Helper methods
    def save_progress(file, data)
      FileUtils.mkdir_p(File.dirname(file))
      File.write(file, JSON.pretty_generate(data))
    end

    def load_progress(file)
      return nil unless File.exist?(file)
      JSON.parse(File.read(file))
    rescue JSON::ParserError
      nil
    end

    # Create raw Meilisearch client (bypassing meilisearch-rails wrappers)
    def raw_meilisearch_client
      @raw_meilisearch_client ||= Meilisearch::Client.new(
        ENV.fetch('MEILI_HOST', 'http://localhost:7700'),
        ENV['MEILI_MASTER_KEY']
      )
    end

    # Create index with settings using direct API (configurable timeout)
    def ensure_index_settings(model_class, timeout_ms = 300_000)
      client = raw_meilisearch_client
      index_uid = model_class.ms_index_uid
      primary_key = model_class.meilisearch_options[:primary_key]&.to_s || 'id'

      puts "    Using direct Meilisearch client (timeout: #{timeout_ms}ms)"

      # Create index if it doesn't exist
      begin
        task = client.create_index(index_uid, { primaryKey: primary_key })
        client.wait_for_task(task['taskUid'], timeout_ms)
        puts "    Created index: #{index_uid}"
      rescue Meilisearch::ApiError => e
        raise e unless e.code == 'index_already_exists'
        puts "    Index already exists: #{index_uid}"
      end

      # Update settings using raw client
      index = client.index(index_uid)
      settings = model_class.meilisearch_settings.to_settings
      puts "    Updating settings..."
      task = index.update_settings(settings)
      puts "    Waiting for task #{task['taskUid']}..."
      client.wait_for_task(task['taskUid'], timeout_ms)
      puts "    Settings updated"
    end

    # Get batch size from environment variable or use default
    batch_size = (ENV['BATCH_SIZE'] || 100).to_i
    resume_mode = ENV['RESUME']&.downcase == 'true'

    puts "Batch size: #{batch_size}"

    models = [
      { name: 'Account', model: Account },
      { name: 'Status', model: Status },
      { name: 'Tag', model: Tag },
      { name: 'Instance', model: Instance },
    ]

    # Load progress if resuming
    progress_data = nil
    start_model_index = 0

    if resume_mode
      progress_data = load_progress(progress_file)
      if progress_data
        start_model_index = progress_data['current_model_index'] || 0
        puts "📥 Resuming from previous run..."
        puts "  → Model: #{progress_data['current_model']}"
        puts "  → Last processed ID: #{progress_data['last_processed_id']}"
        puts "  → Previous timestamp: #{progress_data['timestamp']}"
        puts ''
      else
        puts "⚠ Resume mode enabled but no progress file found. Starting fresh..."
        puts ''
      end
    else
      # Clean start - remove old progress file
      File.delete(progress_file) if File.exist?(progress_file)
    end

    puts 'Starting Meilisearch index deployment...'
    puts ''

    models.each_with_index do |model_info, model_index|
      # Skip models that were already completed
      next if model_index < start_model_index

      model_name = model_info[:name]
      model_class = model_info[:model]

      puts "Reindexing #{model_name}..."
      start_time = Time.now

      begin
        # Create/update index settings (skip on resume for same model - already done)
        is_resuming_same_model = resume_mode && progress_data && progress_data['current_model'] == model_name && progress_data['last_processed_id'].present?
        unless is_resuming_same_model
          puts "  → Creating/updating index settings..."
          ensure_index_settings(model_class)
        else
          puts "  → Skipping index settings (resuming)..."
        end

        puts "  → Counting records..."
        count_start = Time.now
        total_count = model_class.count
        count_elapsed = Time.now - count_start
        puts "  → Found #{total_count} records (took #{count_elapsed.round(2)}s)"

        indexed_count = 0

        # Determine start position
        start_id = nil
        if resume_mode && progress_data &&
           progress_data['current_model'] == model_name &&
           progress_data['last_processed_id']
          start_id = progress_data['last_processed_id']
          puts "  → Resuming from ID: #{start_id}"

          # Restore indexed count from progress data
          indexed_count = progress_data['indexed_count'] || 0
          puts "  → Already processed: #{indexed_count} records"
        end

        progress = ProgressBar.create(
          title: "  #{model_name}",
          total: total_count,
          format: '%t %c/%C |%B| %p%% %e',
          output: $stdout,
          starting_at: indexed_count
        )

        puts "  → Starting batch indexing..."

        # Manually batch index with progress updates
        find_options = { batch_size: batch_size }
        find_options[:start] = start_id + 1 if start_id

        # Track the last successfully processed ID for error recovery
        last_successful_id = start_id

        model_class.find_in_batches(**find_options) do |batch|
          # Filter records that should be indexed
          indexable_records = batch.select { |record| record.respond_to?(:should_index?) ? record.should_index? : true }

          if indexable_records.any?
            # Add documents to index
            model_class.index_documents(indexable_records)
          end

          # Update counters only after successful indexing
          indexed_count += batch.size
          last_successful_id = batch.last.id

          # Prevent progress from exceeding total
          progress.progress = [indexed_count, total_count].min

          # Check for interruption after processing the batch
          if interrupted
            progress_info = {
              'current_model' => model_name,
              'current_model_index' => model_index,
              'last_processed_id' => last_successful_id,
              'batch_size' => batch_size,
              'timestamp' => Time.now.utc.iso8601,
              'indexed_count' => indexed_count,
              'total_count' => total_count
            }
            save_progress(progress_file, progress_info)

            puts "\n"
            puts "💾 Progress saved!"
            puts "  → Model: #{model_name}"
            puts "  → Last processed ID: #{last_successful_id}"
            puts "  → Progress: #{indexed_count}/#{total_count} (#{((indexed_count.to_f / total_count) * 100).round(2)}%)"
            puts "  → Progress file: #{progress_file}"
            puts ""
            puts "To resume, run:"
            puts "  RESUME=true rake meilisearch:deploy"
            puts ""

            exit 0
          end
        end

        progress.finish

        elapsed = Time.now - start_time
        puts "  ✓ Completed in #{elapsed.round(2)} seconds (#{indexed_count} records)"
      rescue StandardError => e
        puts "  ✗ Error: #{e.message}"
        puts e.backtrace.first(5).map { |line| "    #{line}" }.join("\n")

        # Save progress on error as well (only if we have actual progress to save)
        # Use last_successful_id to ensure failed batch will be retried
        if defined?(last_successful_id) && last_successful_id.present?
          progress_info = {
            'current_model' => model_name,
            'current_model_index' => model_index,
            'last_processed_id' => last_successful_id,
            'batch_size' => batch_size,
            'timestamp' => Time.now.utc.iso8601,
            'indexed_count' => indexed_count,
            'total_count' => total_count,
            'error' => e.message
          }
          save_progress(progress_file, progress_info)
          puts ""
          puts "  💾 Progress saved!"
          puts "  → Last successful ID: #{last_successful_id}"
          puts "  → Processed: #{indexed_count}/#{total_count} records"
          puts "  → Progress file: #{progress_file}"
          puts ""
          puts "  ⚠ The failed batch will be retried on resume."
          puts ""
          puts "To resume after fixing the issue, run:"
          puts "  RESUME=true rake meilisearch:deploy"
        end

        raise e
      end

      puts ''
    end

    # Clean up progress file on successful completion
    File.delete(progress_file) if File.exist?(progress_file)

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
