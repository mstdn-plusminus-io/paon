# frozen_string_literal: true

# == Schema Information
#
# Table name: bookmarks
#
#  id         :bigint(8)        not null, primary key
#  account_id :bigint(8)        not null
#  status_id  :bigint(8)        not null
#  created_at :datetime         not null
#  updated_at :datetime         not null
#

class Bookmark < ApplicationRecord
  include Paginable

  belongs_to :account, inverse_of: :bookmarks
  belongs_to :status,  inverse_of: :bookmarks

  validates :status_id, uniqueness: { scope: :account_id }

  before_validation do
    self.status = status.reblog if status&.reblog?
  end

  after_destroy :invalidate_cleanup_info

  # ブックマークフィードキャッシュの更新
  after_create :add_to_bookmark_feed
  after_destroy :remove_from_bookmark_feed

  def invalidate_cleanup_info
    return unless status&.account_id == account_id && account.local?

    account.statuses_cleanup_policy&.invalidate_last_inspected(status, :unbookmark)
  end

  private

  def add_to_bookmark_feed
    BookmarkFeed.new(account).add(self)
  end

  def remove_from_bookmark_feed
    BookmarkFeed.new(account).remove(status_id)
  end
end
