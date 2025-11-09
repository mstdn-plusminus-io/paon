# frozen_string_literal: true

class BookmarkFeed
  include Redisable

  def initialize(account)
    @account = account
  end

  # すべてのブックマークステータスIDを取得
  def get_all_status_ids
    cached_ids = from_redis
    return cached_ids if cached_ids.any?

    # キャッシュミス時はDBから取得してキャッシュ
    warm_cache
    from_redis
  end

  # 特定の範囲のブックマークを取得（ページネーション用）
  def get(limit, max_id = nil, since_id = nil)
    max_id    = max_id.to_i if max_id.present?
    since_id  = since_id.to_i if since_id.present?

    if max_id.present? && since_id.present?
      redis.zrevrangebyscore(key, "(#{max_id}", "(#{since_id}", limit: [0, limit])
    elsif max_id.present?
      redis.zrevrangebyscore(key, "(#{max_id}", '-inf', limit: [0, limit])
    elsif since_id.present?
      redis.zrevrangebyscore(key, '+inf', "(#{since_id}", limit: [0, limit])
    else
      redis.zrevrange(key, 0, limit - 1)
    end.map(&:to_i)
  end

  # キャッシュが存在するか確認
  def cached?
    redis.exists?(key)
  end

  # キャッシュを温める
  def warm_cache
    return if cached?

    bookmark_data = @account.bookmarks
                            .joins(:status)
                            .where(statuses: { deleted_at: nil })
                            .order(id: :desc)
                            .pluck(:status_id, :id)

    return if bookmark_data.empty?

    # バッチでRedisに追加（パフォーマンス最適化）
    redis.pipelined do |pipeline|
      bookmark_data.each do |status_id, bookmark_id|
        # bookmark_idをスコアとして使用（新しいブックマークが大きいスコア）
        pipeline.zadd(key, bookmark_id, status_id)
      end
    end

    # 有効期限を設定（メモリ保護）
    redis.expire(key, cache_ttl)
  end

  # キャッシュに追加
  def add(bookmark)
    redis.zadd(key, bookmark.id, bookmark.status_id)
    redis.expire(key, cache_ttl)
  end

  # キャッシュから削除
  def remove(status_id)
    redis.zrem(key, status_id)
  end

  # キャッシュをクリア
  def clear
    redis.del(key)
  end

  # キャッシュサイズを取得
  def size
    redis.zcard(key)
  end

  private

  def from_redis
    redis.zrevrange(key, 0, -1).map(&:to_i)
  end

  def key
    "feed:bookmark:#{@account.id}"
  end

  def cache_ttl
    bookmark_count = size

    case bookmark_count
    when 0..100
      30.days.to_i  # 小規模: 長期キャッシュ
    when 101..1000
      7.days.to_i   # 中規模: 1週間
    when 1001..10000
      24.hours.to_i # 大規模: 1日
    else
      6.hours.to_i  # 超大規模: 6時間
    end
  end
end