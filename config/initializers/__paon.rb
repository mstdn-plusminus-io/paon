require_relative '../../lib/paon/version'

module I18n
  module Backend
    class CustomSimple < Simple
      def load_yml(filename)
        translations = super

        translations.map do |locale_data|
          if locale_data.is_a?(Hash)
            locale_data.transform_values { |data| process_translations(data) }
          else
            locale_data
          end
        end
      end

      private

      def process_translations(obj)
        case obj
        when Hash
          obj.each_with_object({}) do |(key, value), result|
            result[key] = process_translations(value)
          end
        when String
          obj = obj.gsub('Mastodon', 'Paon')
          obj = obj.gsub('mastodon', 'paon')
          obj.gsub('マストドン', 'ぱおん')
        else
          obj  # boolean, nil, 数値などはそのまま返す
        end
      end
    end
  end
end

I18n.backend = I18n::Backend::CustomSimple.new
