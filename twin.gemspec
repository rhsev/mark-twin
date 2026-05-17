$LOAD_PATH.unshift File.expand_path("lib", __dir__)
require "twin/version"

Gem::Specification.new do |s|
  s.name        = "twin"
  s.version     = Twin::VERSION
  s.summary     = "Sync configuration files between two Macs — CLI with fzf picker"
  s.description = "Reads sync-files (Markdown + YAML blocks) via grubber, " \
                  "groups by program, and runs rsync. Interactive picker uses " \
                  "fzf with apex Markdown preview."
  s.authors     = ["rhsev"]
  s.email       = ["huelsmann@sevelen.net"]
  s.license     = "MIT"
  s.required_ruby_version = ">= 3.1"

  s.files       = Dir["lib/**/*.rb", "bin/*", "README.md", "ARCHITECTURE.md", "LICENSE"]
  s.bindir      = "bin"
  s.executables = ["twin"]
  s.require_paths = ["lib"]

  s.add_development_dependency "minitest", "~> 5.0"
end
