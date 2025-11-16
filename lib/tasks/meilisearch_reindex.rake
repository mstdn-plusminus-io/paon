# frozen_string_literal: true

namespace :meilisearch do
  desc 'Reindex all Status records in Meilisearch'
  task reindex_statuses: :environment do
    if Mastodon.meilisearch_enabled?
      puts "Reindexing Status records in Meilisearch..."

      # Clear the existing index
      begin
        Status.ms_index!.delete_all_documents
        puts "Cleared existing Status index"
      rescue
        puts "No existing index to clear"
      end

      # Reindex all public and unlisted statuses
      batch_size = 1000
      total_count = Status.where(visibility: %i[public unlisted]).count
      processed = 0

      Status.where(visibility: %i[public unlisted]).find_in_batches(batch_size: batch_size) do |batch|
        batch.each(&:ms_index!)
        processed += batch.size
        puts "Indexed #{processed}/#{total_count} statuses..."
      end

      puts "Completed reindexing #{processed} statuses"
    else
      puts "Meilisearch is not enabled"
    end
  end

  desc 'Reindex all records in Meilisearch'
  task reindex_all: :environment do
    if Mastodon.meilisearch_enabled?
      puts "Reindexing all records in Meilisearch..."

      # Reindex Statuses
      Rake::Task['meilisearch:reindex_statuses'].invoke

      # Reindex Accounts
      puts "Reindexing Account records..."
      Account.reindex!
      puts "Completed reindexing Accounts"

      # Reindex Tags
      puts "Reindexing Tag records..."
      Tag.reindex!
      puts "Completed reindexing Tags"

      # Reindex Instances
      puts "Reindexing Instance records..."
      Instance.reindex!
      puts "Completed reindexing Instances"

      puts "All reindexing complete!"
    else
      puts "Meilisearch is not enabled"
    end
  end
end