$LOAD_PATH.unshift File.expand_path("lib", __dir__)
require "twin/version"

Gem::Specification.new do |s|
  s.name        = "grubber-twin"
  s.version     = Twin::VERSION
  s.summary     = "Sync configuration folders between two Macs from self-documenting Markdown files"
  s.description = "twin reads sync-files (Markdown + YAML blocks) via grubber, " \
                  "groups them by program, and runs rsync. Interactive picker uses " \
                  "fzf with an apex Markdown preview."
  s.authors     = ["Ralf Hülsmann"]
  s.email       = ["huelsmann@sevelen.net"]
  s.homepage    = "https://github.com/rhsev/grubber-twin"
  s.license     = "MIT"
  s.required_ruby_version = ">= 3.1"

  s.metadata = {
    "source_code_uri" => "https://github.com/rhsev/grubber-twin",
    "bug_tracker_uri" => "https://github.com/rhsev/grubber-twin/issues",
  }

  s.post_install_message = <<~MSG

    twin requires these external tools in your PATH:
      grubber  https://github.com/rhsev/grubber
      rsync    (preinstalled on macOS)
      fzf      brew install fzf

    Optional for the preview pane:
      apex     https://github.com/ttscoff/apex
      glow / bat as fallbacks (cat is used if none are present)

  MSG

  s.files       = Dir["lib/**/*.rb", "bin/*", "README.md", "ARCHITECTURE.md", "LICENSE"]
  s.bindir      = "bin"
  s.executables = ["twin"]
  s.require_paths = ["lib"]

  s.add_development_dependency "minitest", "~> 5.0"
end
