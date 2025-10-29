# frozen_string_literal: true

module Shakapacker::ManifestExtensions
  def lookup(name, pack_type = {})
    asset = super

    if pack_type[:with_integrity] && asset.respond_to?(:dig)
      [asset.dig('src'), asset.dig('integrity')]
    elsif asset.respond_to?(:dig)
      asset.dig('src')
    else
      asset
    end
  end
end

Shakapacker::Manifest.prepend(Shakapacker::ManifestExtensions)
