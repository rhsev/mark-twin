module Twin
  module Template
    PATTERN = /\{\{([^}]+)\}\}/

    # Fields in a grubber record that may contain {{tokens}}.
    FIELDS = %w[Source Target Path Target-Path Exclude Cmd].freeze

    module_function

    # Replace {{token}} in value using vars. Raises on unknown token.
    def substitute(value, vars, context:)
      return value unless value.is_a?(String)
      value.gsub(PATTERN) do
        token = $1
        vars.fetch(token) { raise "unknown template token {{#{token}}} in #{context}" }
      end
    end

    # Read a template file and substitute {{tokens}} in its content.
    # Binary read preserves bytes exactly; raises on unknown token.
    def render_file(path, vars, context:)
      substitute(File.binread(path), vars, context: context)
    end

    # Return a copy of grubber record r with FIELDS substituted.
    def substitute_record(r, vars, context:)
      return r if vars.empty?
      result = r.dup
      FIELDS.each do |field|
        next unless result.key?(field) && result[field].is_a?(String)
        result[field] = substitute(result[field], vars, context: context)
      end
      result
    end
  end
end
