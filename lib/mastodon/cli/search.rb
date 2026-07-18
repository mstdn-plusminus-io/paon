# frozen_string_literal: true

require_relative 'base'

module Mastodon::CLI
  class Search < Base
    MODELS = [
      Instance,
      Account,
      Tag,
      Status,
    ].freeze

    option :only, type: :array, enum: %w(instances accounts tags statuses), desc: 'Only process these models'
    desc 'deploy', 'Create or populate Meilisearch indices'
    long_desc <<~LONG_DESC
      Populate Meilisearch indices with data from the database.

      Meilisearch automatically creates and manages indices, so this command
      simply triggers a reindex of the specified models.
    LONG_DESC
    def deploy
      unless Mastodon.meilisearch_enabled?
        say('Meilisearch is not enabled', :red)
        exit(1)
      end

      models = if options[:only]
                 options[:only].map { |str| str.camelize.constantize }
               else
                 MODELS
               end

      models.each do |model|
        say("Reindexing #{model}...", :yellow)
        model.reindex
        say("Done reindexing #{model}", :green)
      end

      say('All indices have been populated!', :green, true)
    end
  end
end
