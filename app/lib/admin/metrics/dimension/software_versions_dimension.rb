# frozen_string_literal: true

class Admin::Metrics::Dimension::SoftwareVersionsDimension < Admin::Metrics::Dimension::BaseDimension
  include Redisable

  def key
    'software_versions'
  end

  protected

  def perform_query
    [mastodon_version, ruby_version, rails_version, postgresql_version, redis_version, meilisearch_version].compact
  end

  def mastodon_version
    value = Paon::Version.to_s

    {
      key: 'mastodon',
      human_key: 'Paon',
      value: value,
      human_value: value,
    }
  end

  def ruby_version
    value = "#{RUBY_VERSION}p#{RUBY_PATCHLEVEL}"

    {
      key: 'ruby',
      human_key: 'Ruby',
      value: value,
      human_value: value,
    }
  end

  def rails_version
    value = Rails::VERSION::STRING.to_s

    {
      key: 'rails',
      human_key: 'Rails',
      value: value,
      human_value: value,
    }
  end

  def postgresql_version
    value = ActiveRecord::Base.connection.execute('SELECT VERSION()').first['version'].match(/\A(?:PostgreSQL |)([^\s]+).*\z/)[1]

    {
      key: 'postgresql',
      human_key: 'PostgreSQL',
      value: value,
      human_value: value,
    }
  end

  def redis_version
    value = redis_info['redis_version']

    {
      key: 'redis',
      human_key: 'Redis',
      value: value,
      human_value: value,
    }
  end

  def meilisearch_version
    return unless Mastodon.meilisearch_enabled?

    client = MeiliSearch::Client.new(
      ENV.fetch('MEILI_HOST') { 'http://localhost:7700' },
      ENV.fetch('MEILI_MASTER_KEY') { nil }
    )
    version_info = client.version
    version = version_info['pkgVersion']

    {
      key: 'meilisearch',
      human_key: 'Meilisearch',
      value: version,
      human_value: version,
    }
  rescue Faraday::ConnectionFailed, StandardError
    nil
  end

  def redis_info
    @redis_info ||= if redis.is_a?(Redis::Namespace)
                      redis.redis.info
                    else
                      redis.info
                    end
  end
end
