require "json"
require "fileutils"
require "time"

module Twin
  # Append-only sync journal: one JSON line per synced job in
  # ~/.local/state/twin/log.jsonl. Answers "did yesterday's sync actually
  # run, and what did it do?" — and stays machine-readable (jq/grubber).
  # Journal failures never break a sync; they degrade to a warning.
  module Journal
    module_function

    def state_dir
      ENV["TWIN_STATE_DIR"] || File.join(Dir.home, ".local", "state", "twin")
    end

    def log_path = File.join(state_dir, "log.jsonl")

    # Record one job result. Dry-runs are not journaled.
    def record(job, success:, transferred:, output: nil)
      entry = {
        ts:      Time.now.iso8601,
        program: job.program,
        path:    job.path,
        target:  job.target,
        ok:      success,
        changed: transferred,
      }
      unless success
        entry[:error] = output.to_s.lines.map(&:strip).reject(&:empty?).last.to_s[0, 200]
      end
      FileUtils.mkdir_p(state_dir)
      File.open(log_path, "a") { |f| f.puts(JSON.generate(entry)) }
    rescue SystemCallError => e
      warn "journal: #{e.message}" unless @warned
      @warned = true
    end

    # Last n entries, oldest first. Unparseable lines are skipped.
    def tail(n)
      return [] unless File.exist?(log_path)
      File.readlines(log_path).last(n).filter_map do |line|
        JSON.parse(line)
      rescue JSON::ParserError
        nil
      end
    end
  end
end
