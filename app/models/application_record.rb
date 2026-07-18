# frozen_string_literal: true

class ApplicationRecord < ActiveRecord::Base
  self.abstract_class = true

  include Remotable

  connects_to database: { writing: :primary, reading: :replica } if DatabaseHelper.replica_enabled?

  def boolean_with_default(key, default_value)
    value = attributes[key]

    if value.nil?
      default_value
    else
      value
    end
  end
end
