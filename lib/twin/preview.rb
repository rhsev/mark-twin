module Twin
  # Compact markdown preview for a single sync block: frontmatter +
  # intro (text before the first heading) + the heading section that
  # contains the YAML block for *target_path*.
  module Preview
    module_function

    YAML_FENCE = /\A```ya?ml\s*\z/.freeze
    FENCE_END  = /\A```\s*\z/.freeze
    HEADING    = /\A(\#{1,6})\s/.freeze
    H2_PLUS    = /\A\#{2,6}\s/.freeze
    PATH_KEY   = /\APath:\s*['"]?([^'"]+?)['"]?\s*\z/.freeze

    def extract_compact(file_path, target_path)
      content = File.read(file_path, encoding: "UTF-8")

      frontmatter, body = split_frontmatter(content)
      lines = body.lines.map(&:chomp)

      intro     = extract_intro(lines)
      section   = extract_section_for(lines, target_path)

      [frontmatter, intro, section].reject(&:empty?).join("\n\n") + "\n"
    rescue Errno::ENOENT
      ""
    end

    def split_frontmatter(content)
      return ["", content] unless content.start_with?("---\n") || content.start_with?("---\r\n")
      parts = content.split(/^---\s*$\n/, 3)
      return ["", content] if parts.size < 3
      ["---\n#{parts[1]}---", parts[2]]
    end

    # Intro = everything up to the first H2 (H1 + body counts as document title).
    def extract_intro(lines)
      idx = lines.index { |l| l =~ H2_PLUS }
      idx = lines.size unless idx
      lines[0...idx].join("\n").strip
    end

    # Locate the YAML block whose Path: matches *target*, then return the
    # surrounding heading section (nearest preceding heading until the next
    # heading of equal-or-higher level).
    def extract_section_for(lines, target)
      block_start, block_end = find_block(lines, target)
      return "" unless block_start

      section_start = block_start
      (block_start - 1).downto(0) do |i|
        if lines[i] =~ HEADING
          section_start = i
          break
        end
      end

      section_end = lines.size - 1
      if (m = lines[section_start]&.match(HEADING))
        level = m[1].length
        ((block_end + 1)...lines.size).each do |i|
          if (m2 = lines[i].match(HEADING)) && m2[1].length <= level
            section_end = i - 1
            break
          end
        end
      end

      lines[section_start..section_end].join("\n").strip
    end

    def find_block(lines, target)
      i = 0
      while i < lines.size
        if lines[i] =~ YAML_FENCE
          k = i + 1
          matched = false
          while k < lines.size && lines[k] !~ FENCE_END
            if (m = lines[k].match(PATH_KEY)) && m[1] == target
              matched = true
            end
            k += 1
          end
          return [i, k] if matched
          i = k + 1
        else
          i += 1
        end
      end
      nil
    end
  end
end
