require "yaml"
require "pathname"

module Twin
  class Config
    attr_accessor :sync_dir, :global_excludes,
                  :apex_theme, :apex_width,
                  :apex_code_highlight, :apex_code_highlight_theme

    DEFAULTS = {
      "global_excludes"           => [".DS_Store"],
      "apex_theme"                => nil,
      "apex_width"                => nil,
      "apex_code_highlight"       => nil,
      "apex_code_highlight_theme" => nil,
    }.freeze

    def initialize(data = {})
      merged = DEFAULTS.merge(data || {})
      @sync_dir                  = ENV["TWIN_SYNC_DIR"] || merged["sync_dir"].to_s
      @global_excludes           = merged["global_excludes"] || []
      @apex_theme                = merged["apex_theme"]
      @apex_width                = merged["apex_width"]
      @apex_code_highlight       = merged["apex_code_highlight"]
      @apex_code_highlight_theme = merged["apex_code_highlight_theme"]
    end

    def self.load
      path = ENV["TWIN_CONFIG"] || File.join(Dir.home, ".config", "twin", "config.yaml")
      data = {}
      if File.exist?(path)
        begin
          data = YAML.safe_load_file(path) || {}
        rescue Psych::SyntaxError => e
          raise "config syntax error in #{path}: #{e.message}"
        end
      end
      new(data)
    end

    def validate!
      raise "sync_dir not set (add to ~/.config/twin/config.yaml or set TWIN_SYNC_DIR)" if sync_dir.empty?
      raise "sync_dir not found: #{sync_dir}" unless Dir.exist?(sync_dir)
    end
  end
end
