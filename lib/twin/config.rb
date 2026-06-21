require "yaml"
require "pathname"

module Twin
  class Config
    attr_accessor :sync_dir, :global_excludes,
                  :apex_theme, :apex_width,
                  :apex_code_highlight, :apex_code_highlight_theme,
                  :hosts, :host, :target

    DEFAULTS = {
      "global_excludes"           => [".DS_Store"],
      "apex_theme"                => nil,
      "apex_width"                => nil,
      "apex_code_highlight"       => nil,
      "apex_code_highlight_theme" => nil,
      "hosts"                     => {},
      "host"                      => "",
      "target"                    => "",
    }.freeze

    def initialize(data = {})
      merged = DEFAULTS.merge(data || {})
      @sync_dir                  = ENV["TWIN_SYNC_DIR"] || merged["sync_dir"].to_s
      @global_excludes           = merged["global_excludes"] || []
      @apex_theme                = merged["apex_theme"]
      @apex_width                = merged["apex_width"]
      @apex_code_highlight       = merged["apex_code_highlight"]
      @apex_code_highlight_theme = merged["apex_code_highlight_theme"]
      @hosts                     = merged["hosts"] || {}
      @host                      = ENV["TWIN_HOST"] || merged["host"].to_s
      @target                    = merged["target"].to_s
    end

    # Build the flat substitution map {src.home => ..., dst.home => ..., dst.mount => ...}.
    # Returns empty hash when no hosts are configured (substitution becomes a no-op).
    def var_map
      return {} if hosts.empty?
      raise "host not set in config"   if host.empty?
      raise "target not set in config" if target.empty?

      src_host = hosts[host]
      dst_host = hosts[target]
      raise "unknown host #{host.inspect} (not in hosts)"     unless src_host
      raise "unknown target #{target.inspect} (not in hosts)" unless dst_host

      map = {}
      src_host.each { |k, v| map["src.#{k}"] = v.to_s }
      dst_host.each { |k, v| map["dst.#{k}"] = v.to_s }
      map
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
