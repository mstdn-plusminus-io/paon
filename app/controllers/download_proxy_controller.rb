# frozen_string_literal: true

class DownloadProxyController < ApplicationController
  include RoutingHelper
  include Authorization
  include Redisable
  include Lockable

  skip_before_action :require_functional!

  before_action :authenticate_user!, if: :limited_federation_mode?

  rescue_from ActiveRecord::RecordInvalid, with: :not_found
  rescue_from Mastodon::UnexpectedResponseError, with: :not_found
  rescue_from Mastodon::NotPermittedError, with: :not_found
  rescue_from HTTP::TimeoutError, HTTP::ConnectionError, OpenSSL::SSL::SSLError, with: :internal_server_error

  def show
    with_redis_lock("media_download:#{params[:id]}") do
      @media_attachment = MediaAttachment.attached.find(params[:id])
      authorize @media_attachment.status, :show?
      redownload! if @media_attachment.needs_redownload? && !reject_media?
    end

    # Set proper CORS headers
    response.headers['Access-Control-Allow-Origin'] = '*'
    response.headers['Access-Control-Allow-Methods'] = 'GET, HEAD, OPTIONS'
    response.headers['Access-Control-Allow-Headers'] = 'Content-Type, Authorization'

    # Stream file content directly
    file_url = @media_attachment.file.url(version)
    filename = extract_filename_from_url(file_url)

    # Set CORS headers
    response.headers['Access-Control-Allow-Origin'] = '*'
    response.headers['Access-Control-Allow-Methods'] = 'GET, HEAD, OPTIONS'
    response.headers['Access-Control-Allow-Headers'] = 'Content-Type, Authorization'
    response.headers['Content-Disposition'] = "attachment; filename=\"#{filename}\""
    response.headers['Content-Type'] = @media_attachment.file.content_type

    # Stream the file content
    # Stream file content directly
    if Rails.configuration.x.use_s3 || Rails.configuration.x.use_swift
      # For S3/Swift, stream from remote URL
      require 'open-uri'
      file_content = URI.open(file_url).read
      send_data file_content,
                type: @media_attachment.file.content_type,
                filename: filename,
                disposition: 'attachment'
    else
      # For local storage
      send_file @media_attachment.file.path(version),
                type: @media_attachment.file.content_type,
                filename: filename,
                disposition: 'attachment'
    end
  end

  private

  def redownload!
    @media_attachment.download_file!
    @media_attachment.created_at = Time.now.utc
    @media_attachment.save!
  end

  def version
    if request.path.end_with?('/small')
      :small
    else
      :original
    end
  end

  def reject_media?
    DomainBlock.reject_media?(@media_attachment.account.domain)
  end

  def extract_filename
    if @media_attachment.file.present?
      extract_filename_from_url(@media_attachment.file.url(version))
    elsif @media_attachment.remote_url.present?
      extract_filename_from_url(@media_attachment.remote_url)
    else
      'media'
    end
  end

  def extract_filename_from_url(url)
    URI.parse(url).path.split('/').last
  rescue
    'media'
  end

  def stream_from_remote_url
    # For remote media that hasn't been cached locally
    return not_found unless @media_attachment.remote_url.present?

    require 'open-uri'
    file_content = URI.open(@media_attachment.remote_url).read
    send_data file_content,
              type: @media_attachment.file_content_type || 'application/octet-stream',
              filename: extract_filename,
              disposition: 'attachment'
  rescue OpenURI::HTTPError, URI::InvalidURIError => e
    Rails.logger.error "Failed to stream remote media: #{e.message}"
    not_found
  end

  def stream_from_object_storage
    # For S3/Swift storage
    file_url = @media_attachment.file.url(version)
    require 'open-uri'
    file_content = URI.open(file_url).read
    send_data file_content,
              type: @media_attachment.file.content_type,
              filename: extract_filename,
              disposition: 'attachment'
  rescue OpenURI::HTTPError, URI::InvalidURIError => e
    Rails.logger.error "Failed to stream from object storage: #{e.message}"
    not_found
  end

  def stream_from_local_file
    # For local filesystem storage
    file_path = @media_attachment.file.path(version)

    # Check if the file actually exists on disk
    if File.exist?(file_path)
      send_file file_path,
                type: @media_attachment.file.content_type,
                filename: extract_filename,
                disposition: 'attachment'
    elsif @media_attachment.remote_url.present?
      # Fallback to streaming from remote URL if local file doesn't exist
      stream_from_remote_url
    else
      Rails.logger.error "Local file not found and no remote URL available: #{file_path}"
      not_found
    end
  end

  def not_found
    render json: { error: 'Not found' }, status: :not_found
  end

  def internal_server_error
    render json: { error: 'Internal server error' }, status: :internal_server_error
  end
end
