$LOAD_PATH.unshift File.expand_path("lib", __dir__)
require "twin/version"

Gem::Specification.new do |s|
  s.name        = "mark-twin"
  s.version     = Twin::VERSION
  s.summary     = "Sync configuration between Macs and any ssh host, from self-documenting Markdown files"
  s.description = <<~DESC
    twin helps keep two machines' configuration aligned.

    A sync-file is just Markdown: the prose says why a path is synced, and
    fenced YAML blocks define what to do. rsync does the copying.

    When both sides changed since the last sync, twin shows a diff and asks
    before overwriting. A target is a local path, a mounted volume, or
    user@host:/path over ssh, and twin treats all three the same.

    Interactive selection runs through fzf with a rendered Markdown preview; the
    same sync-files drive scriptable status, dry-run and sync commands.
  DESC
  s.authors     = ["Ralf Hülsmann"]
  s.email       = ["huelsmann@sevelen.net"]
  s.homepage    = "https://github.com/rhsev/mark-twin"
  s.license     = "MIT"
  s.required_ruby_version = ">= 3.1"

  s.metadata = {
    "source_code_uri"   => "https://github.com/rhsev/mark-twin",
    "bug_tracker_uri"   => "https://github.com/rhsev/mark-twin/issues",
    "changelog_uri"     => "https://github.com/rhsev/mark-twin/releases",
    "documentation_uri" => "https://github.com/rhsev/mark-twin#readme",
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
