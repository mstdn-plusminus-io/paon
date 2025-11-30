# frozen_string_literal: true

class MeilisearchQueryTransformer < Parslet::Transform
  attr_accessor :current_account

  SUPPORTED_PREFIXES = %w(
    has
    is
    language
    from
    before
    after
    during
    in
  ).freeze

  class Query
    def initialize(clauses, options = {})
      raise ArgumentError if options[:current_account].nil?

      @clauses = clauses
      @options = options
      @query_terms = []
      @meilisearch_filters = []
      @postgresql_filters = []
      @sort = []
      @bookmark_mode = false

      process_clauses!
      build_visibility_filter!
    end

    def to_meilisearch_query
      {
        query: query_string,
        filter: @final_filter_string,
        sort: sort_array
      }.compact.reject { |_, v| v.blank? }
    end

    # Returns conditions for PostgreSQL post-filtering
    def to_postgresql_conditions
      @postgresql_filters.compact
    end

    def bookmark_mode?
      @bookmark_mode
    end

    def current_account
      @options[:current_account]
    end

    private

    def process_clauses!
      @clauses.compact.each do |clause|
        case clause
        when TermClause, PhraseClause
          if clause.operator == :must_not
            # Meilisearch doesn't support negative queries directly in search
            # We'll need to handle this differently
            @query_terms << "-#{clause.to_query_string}"
          else
            @query_terms << clause.to_query_string
          end
        when PrefixClause
          if clause.operator == :flag
            # Handle special flags like 'in:'
            @flags ||= {}
            @flags[clause.prefix] = clause.term
          elsif clause.operator == :meilisearch_filter
            # Filters handled by Meilisearch (account_id, created_at_timestamp)
            @meilisearch_filters << clause.to_filter_string
          elsif clause.operator == :postgresql_filter
            # Filters handled by PostgreSQL post-filtering (has:*, is:*, language:)
            @postgresql_filters << clause.to_postgresql_condition
          end
        end
      end
    end

    def query_string
      @query_terms.join(' ').strip
    end

    def build_visibility_filter!
      filters = @meilisearch_filters.dup
      current_account_id = @options[:current_account].id

      # Add visibility filter based on 'in:' flag
      case @flags&.dig('in')
      when 'library'
        # User's own posts only
        filters << "account_id = #{current_account_id}"
      when 'public'
        # Public posts only
        filters << 'visibility = "public"'
      when 'bookmark'
        # User's bookmarked posts - handled separately in search service
        @bookmark_mode = true
      else
        # Default (in:all): public posts OR user's own posts
        filters << "(visibility = \"public\" OR account_id = #{current_account_id})"
      end

      @final_filter_string = filters.compact.join(' AND ')
    end

    def sort_array
      # Default sort by created_at timestamp descending
      ['created_at_timestamp:desc']
    end
  end

  class Operator
    class << self
      def symbol(str)
        case str
        when '+', nil
          :must
        when '-'
          :must_not
        else
          raise "Unknown operator: #{str}"
        end
      end
    end
  end

  class TermClause
    attr_reader :operator, :term

    def initialize(operator, term)
      @operator = Operator.symbol(operator)
      @term = term
    end

    def to_query_string
      if @term.start_with?('#')
        # Search in tags
        @term.delete_prefix('#')
      else
        @term
      end
    end
  end

  class PhraseClause
    attr_reader :operator, :phrase

    def initialize(operator, phrase)
      @operator = Operator.symbol(operator)
      @phrase = phrase
    end

    def to_query_string
      "\"#{@phrase}\""
    end
  end

  class PrefixClause
    attr_reader :operator, :prefix, :term

    def initialize(prefix, operator, term, options = {})
      @prefix = prefix
      @negated = operator == '-'
      @options = options

      case prefix
      when 'has'
        parse_has_filter(term)
        @operator = :postgresql_filter
      when 'is'
        parse_is_filter(term)
        @operator = :postgresql_filter
      when 'language'
        @filter = :language
        @term = language_code_from_term(term)
        @operator = :postgresql_filter
      when 'from'
        @filter = :account_id
        @term = account_id_from_term(term)
        @operator = :meilisearch_filter
      when 'before'
        @filter = :created_at_timestamp
        @term = timestamp_from_date(term, :before)
        @operator = :meilisearch_filter
      when 'after'
        @filter = :created_at_timestamp
        @term = timestamp_from_date(term, :after)
        @operator = :meilisearch_filter
      when 'during'
        @filter = :created_at_timestamp
        @term = timestamp_from_date(term, :during)
        @operator = :meilisearch_filter
      when 'in'
        @operator = :flag
        @term = term
      else
        raise "Unknown prefix: #{prefix}"
      end
    end

    # For Meilisearch filters (account_id, created_at_timestamp)
    def to_filter_string
      return nil unless @operator == :meilisearch_filter

      case @filter
      when :created_at_timestamp
        case @prefix
        when 'before'
          "created_at_timestamp < #{@term}"
        when 'after'
          "created_at_timestamp >= #{@term}"
        when 'during'
          start_time = @term
          end_time = start_time + 86400 # Add one day in seconds
          "created_at_timestamp >= #{start_time} AND created_at_timestamp < #{end_time}"
        end
      when :account_id
        if @negated
          "account_id != #{@term}"
        else
          "account_id = #{@term}"
        end
      else
        nil
      end
    end

    # For PostgreSQL post-filtering (has:*, is:*, language:)
    def to_postgresql_condition
      return nil unless @operator == :postgresql_filter

      case @filter
      when :language
        { type: :language, value: @term, negated: @negated }
      when :has_media
        { type: :has_media, negated: @negated }
      when :has_image
        { type: :has_image, negated: @negated }
      when :has_video
        { type: :has_video, negated: @negated }
      when :has_poll
        { type: :has_poll, negated: @negated }
      when :has_link
        { type: :has_link, negated: @negated }
      when :has_embed
        { type: :has_embed, negated: @negated }
      when :sensitive
        { type: :sensitive, negated: @negated }
      when :is_reply
        { type: :is_reply, negated: @negated }
      else
        nil
      end
    end

    private

    def parse_has_filter(term)
      case term
      when 'media'
        @filter = :has_media
      when 'image'
        @filter = :has_image
      when 'video'
        @filter = :has_video
      when 'poll'
        @filter = :has_poll
      when 'link'
        @filter = :has_link
      when 'embed'
        @filter = :has_embed
      else
        raise "Unknown has: filter: #{term}"
      end
    end

    def parse_is_filter(term)
      case term
      when 'reply'
        @filter = :is_reply
      when 'sensitive'
        @filter = :sensitive
      else
        # Treat 'is:' the same as 'has:' for compatibility
        parse_has_filter(term)
      end
    end

    def account_id_from_term(term)
      return @options[:current_account]&.id || -1 if term == 'me'

      username, domain = term.gsub(/\A@/, '').split('@')
      domain = nil if TagManager.instance.local_domain?(domain)
      account = Account.find_remote(username, domain)

      # If the account is not found, we want to return empty results, so return
      # an ID that does not exist
      account&.id || -1
    end

    def language_code_from_term(term)
      language_code = term

      return language_code if LanguagesHelper::SUPPORTED_LOCALES.key?(language_code.to_sym)

      language_code = term.downcase

      return language_code if LanguagesHelper::SUPPORTED_LOCALES.key?(language_code.to_sym)

      language_code = term.split(/[_-]/).first.downcase

      return language_code if LanguagesHelper::SUPPORTED_LOCALES.key?(language_code.to_sym)

      term
    end

    def timestamp_from_date(date_string, type)
      # Parse the date string and convert to timestamp
      begin
        date = Date.parse(date_string)
        time_zone = @options[:current_account]&.user_time_zone.presence || 'UTC'

        case type
        when :before
          # Before means end of previous day
          date.in_time_zone(time_zone).beginning_of_day.to_i
        when :after
          # After means beginning of day
          date.in_time_zone(time_zone).beginning_of_day.to_i
        when :during
          # During means beginning of day (end handled in to_filter_string)
          date.in_time_zone(time_zone).beginning_of_day.to_i
        end
      rescue ArgumentError
        # If date parsing fails, return current timestamp
        Time.current.to_i
      end
    end
  end

  rule(clause: subtree(:clause)) do
    prefix   = clause[:prefix][:term].to_s.downcase if clause[:prefix]
    operator = clause[:operator]&.to_s
    term     = clause[:phrase] ? clause[:phrase].map { |term| term[:term].to_s }.join(' ') : clause[:term].to_s

    if clause[:prefix] && SUPPORTED_PREFIXES.include?(prefix)
      PrefixClause.new(prefix, operator, term, current_account: current_account)
    elsif clause[:prefix]
      TermClause.new(operator, "#{prefix} #{term}")
    elsif clause[:term]
      TermClause.new(operator, term)
    elsif clause[:phrase]
      PhraseClause.new(operator, term)
    else
      raise "Unexpected clause type: #{clause}"
    end
  end

  rule(junk: subtree(:junk)) do
    nil
  end

  rule(query: sequence(:clauses)) do
    Query.new(clauses, current_account: current_account)
  end
end