#!/usr/bin/env ruby
# Test script for in:bookmark search functionality

require_relative 'config/environment'

puts "Testing in:bookmark Search Functionality"
puts "=" * 50

# Find a test account
account = Account.local.first
unless account
  puts "No local account found. Creating test account..."
  account = Account.create!(
    username: 'test_user',
    domain: nil
  )
end
puts "Using account: @#{account.username} (ID: #{account.id})"

# Create test statuses
test_statuses = []
puts "\nCreating test statuses..."
3.times do |i|
  status = Status.create!(
    account: account,
    text: "Test status #{i + 1} for bookmark search #{Time.now.to_i}",
    visibility: :public
  )
  test_statuses << status
  puts "  Created: Status ##{status.id}"
end

# Create bookmarks for some statuses
puts "\nCreating bookmarks..."
bookmarked_status_1 = test_statuses[0]
bookmarked_status_2 = test_statuses[1]
unbookmarked_status = test_statuses[2]

bookmark1 = Bookmark.create!(account: account, status: bookmarked_status_1)
puts "  Bookmarked: Status ##{bookmarked_status_1.id}"

bookmark2 = Bookmark.create!(account: account, status: bookmarked_status_2)
puts "  Bookmarked: Status ##{bookmarked_status_2.id}"

puts "  Not bookmarked: Status ##{unbookmarked_status.id}"

# Test BookmarkFeed
puts "\n1. Testing BookmarkFeed"
feed = BookmarkFeed.new(account)

puts "  Cache exists?: #{feed.cached?}"
puts "  Cache size: #{feed.size}"

bookmarked_ids = feed.get_all_status_ids
puts "  Bookmarked status IDs: #{bookmarked_ids.inspect}"
puts "  Expected: [#{bookmarked_status_2.id}, #{bookmarked_status_1.id}] (newest first)"
puts "  ✅ BookmarkFeed working" if bookmarked_ids == [bookmarked_status_2.id, bookmarked_status_1.id]

# Wait for Meilisearch indexing
puts "\nWaiting for Meilisearch indexing..."
sleep(2)

# Test search service
service = StatusesSearchService.new

puts "\n2. Testing 'in:bookmark' search"
results = service.call('in:bookmark', account, limit: 10)
puts "  Results count: #{results.count}"
result_ids = results.map(&:id).sort
expected_ids = [bookmarked_status_1.id, bookmarked_status_2.id].sort
puts "  Result IDs: #{result_ids.inspect}"
puts "  Expected IDs: #{expected_ids.inspect}"
puts "  ✅ Only bookmarked statuses returned" if result_ids == expected_ids

puts "\n3. Testing 'in:bookmark test' (with keyword)"
results = service.call('in:bookmark test', account, limit: 10)
puts "  Results count: #{results.count}"
puts "  All results contain 'test': #{results.all? { |s| s.text.downcase.include?('test') }}"

puts "\n4. Testing 'in:bookmark from:me'"
results = service.call('in:bookmark from:me', account, limit: 10)
puts "  Results count: #{results.count}"
puts "  All from current user: #{results.all? { |s| s.account_id == account.id }}"

puts "\n5. Testing cache invalidation"
puts "  Removing bookmark for Status ##{bookmarked_status_1.id}..."
bookmark1.destroy
sleep(1)

feed = BookmarkFeed.new(account)
bookmarked_ids = feed.get_all_status_ids
puts "  Updated bookmarked IDs: #{bookmarked_ids.inspect}"
puts "  Expected: [#{bookmarked_status_2.id}]"
puts "  ✅ Cache invalidation working" if bookmarked_ids == [bookmarked_status_2.id]

results = service.call('in:bookmark', account, limit: 10)
puts "  Search results after unbookmark: #{results.count}"
puts "  ✅ Search reflects unbookmark" if results.count == 1 && results.first.id == bookmarked_status_2.id

puts "\n6. Testing empty bookmarks"
bookmark2.destroy
sleep(1)

results = service.call('in:bookmark', account, limit: 10)
puts "  Results with no bookmarks: #{results.count}"
puts "  ✅ Returns empty results" if results.empty?

# Test with another user's bookmarks
puts "\n7. Testing privacy (other user's bookmarks)"
other_account = Account.where.not(id: account.id).first || Account.create!(
  username: 'other_user',
  domain: nil
)
other_bookmark = Bookmark.create!(account: other_account, status: unbookmarked_status)
puts "  Other user bookmarked Status ##{unbookmarked_status.id}"

results = service.call('in:bookmark', account, limit: 10)
puts "  Current user's bookmark search: #{results.count}"
puts "  ✅ Privacy maintained" if results.empty?

# Cleanup
puts "\nCleaning up test data..."
test_statuses.each(&:destroy)
other_bookmark.destroy if other_bookmark

puts "\n✅ All tests completed!"