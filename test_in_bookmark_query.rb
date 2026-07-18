# Test in:bookmark query parsing

require_relative 'config/environment'

account = Account.local.first
puts "Using account: @#{account.username}"

# Create test bookmarks if needed
status = Status.where(account: account).first || Status.create!(
  account: account,
  text: "Test status for bookmark search",
  visibility: :public
)
Bookmark.find_or_create_by!(account: account, status: status)
puts "Ensured bookmark exists for status ##{status.id}"

# Parse query with MeilisearchQueryTransformer
parser = SearchQueryParser.new
transformer = MeilisearchQueryTransformer.new
transformer.current_account = account

queries_to_test = [
  "in:bookmark",
  "in:bookmark test",
  "test in:bookmark",
]

queries_to_test.each do |query_string|
  puts "\n" + "=" * 50
  puts "Query: '#{query_string}'"
  
  begin
    tree = parser.parse(query_string)
    result = transformer.apply(tree)
    meilisearch_query = result.to_meilisearch_query
    
    puts "Parsed result:"
    puts "  Query string: #{meilisearch_query[:query].inspect}"
    puts "  Filter: #{meilisearch_query[:filter].inspect}"
    puts "  Sort: #{meilisearch_query[:sort].inspect}"
  rescue StandardError => e
    puts "Parse error: #{e.message}"
  end
end

# Test the actual search service
service = StatusesSearchService.new
puts "\n" + "=" * 50
puts "Testing actual search with in:bookmark"
results = service.call('in:bookmark', account, limit: 10)
puts "Results count: #{results.count}"
puts "Results: #{results.map(&:id).inspect}" if results.any?
