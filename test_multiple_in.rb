require_relative 'config/environment'

account = Account.local.first
puts "Using account: @#{account.username}"

# 複数のin:句を含むクエリをテスト
queries_with_multiple_in = [
  "in:bookmark in:library",
  "in:library in:bookmark",
  "in:bookmark in:all",
  "in:all in:bookmark",
]

queries_with_multiple_in.each do |query|
  puts "\n" + "=" * 50
  puts "Query: '#{query}'"
  
  parser = SearchQueryParser.new
  transformer = MeilisearchQueryTransformer.new
  transformer.current_account = account
  
  begin
    tree = parser.parse(query)
    parsed_query = transformer.apply(tree, current_account: account)
    meilisearch_params = parsed_query.to_meilisearch_query
    
    puts "Generated filter: #{meilisearch_params[:filter].inspect}"
    puts "Query string: #{meilisearch_params[:query].inspect}"
    
    # 実際に検索実行
    service = StatusesSearchService.new
    results = service.call(query, account, limit: 10)
    puts "Results count: #{results.count}"
  rescue => e
    puts "Error: #{e.message}"
  end
end

# クリアな比較のため、単独のin:bookmarkも再テスト
puts "\n" + "=" * 50
puts "Control test - Query: 'in:bookmark' (alone)"
service = StatusesSearchService.new
results = service.call('in:bookmark', account, limit: 10)
puts "Results count: #{results.count}"
puts "Result IDs: #{results.map(&:id).inspect}" if results.any?
