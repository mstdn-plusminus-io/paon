require_relative 'config/environment'

account = Account.local.first
puts "Using account: @#{account.username}"

# テスト用のブックマーク作成
status = Status.where(account: account).first
Bookmark.find_or_create_by!(account: account, status: status) if status

# StatusesSearchServiceのテスト
service = StatusesSearchService.new

queries = [
  "in:bookmark",
  "test",
  "in:bookmark test",
  "in:all test",
  "in:library test"
]

queries.each do |query|
  puts "\n" + "=" * 50
  puts "Query: '#{query}'"
  
  # search_with_filtersのロジックをシミュレート
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
    results = service.call(query, account, limit: 10)
    puts "Results count: #{results.count}"
  rescue => e
    puts "Error: #{e.message}"
  end
end
