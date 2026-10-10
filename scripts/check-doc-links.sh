#!/usr/bin/env bash
# Checks the relative links in Markdown files: every linked file or directory
# exists, and every #anchor names a heading in the linked Markdown file (as
# GitHub derives anchors: lowercase, punctuation removed, spaces to hyphens,
# -N suffixes for repeats). External links (scheme:, //) are not fetched.
#
# Usage: scripts/check-doc-links.sh [FILE.md...]   (default: every *.md that
#   Git tracks or would track)
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

if (($# == 0)); then
	mapfile -t files < <(git ls-files -co --exclude-standard '*.md')
else
	files=("$@")
fi

# anchors FILE prints the anchors of FILE's headings, outside code fences.
anchors() {
	awk '
		/^[[:space:]]*(```|~~~)/ { fence = !fence; next }
		fence { next }
		/^#{1,6}[[:space:]]/ {
			h = $0
			sub(/^#+[[:space:]]+/, "", h); sub(/[[:space:]]+#+[[:space:]]*$/, "", h)
			gsub(/`/, "", h)
			h = tolower(h)
			gsub(/[^a-z0-9 _-]/, "", h)
			gsub(/ /, "-", h)
			n = seen[h]++
			print (n ? h "-" n : h)
		}' "$1"
}

broken=0 checked=0
for f in "${files[@]}"; do
	dir="$(dirname "$f")"
	# Inline links and images, outside code fences and inline code spans.
	while IFS= read -r target; do
		[[ -z "$target" || "$target" =~ ^[a-zA-Z][a-zA-Z0-9+.-]*: || "$target" == //* ]] && continue
		checked=$((checked + 1))
		path=${target%%#*} anchor=""
		path=${path//%20/ }
		[[ "$target" == *"#"* ]] && anchor=${target#*#}
		if [[ -z "$path" ]]; then
			resolved="$f"
		else
			resolved="$dir/$path"
		fi
		if [[ ! -e "$resolved" ]]; then
			echo "$f: broken link $target (no $resolved)"
			broken=$((broken + 1))
			continue
		fi
		if [[ -n "$anchor" && "$resolved" == *.md ]] && ! anchors "$resolved" | grep -qxF -- "$anchor"; then
			echo "$f: broken link $target (no heading #$anchor in $resolved)"
			broken=$((broken + 1))
		fi
	done < <(awk '
		/^[[:space:]]*(```|~~~)/ { fence = !fence; next }
		fence { next }
		{
			line = $0
			gsub(/`[^`]*`/, "", line)
			while (match(line, /\]\([^)[:space:]]+(\)|[[:space:]])/)) {
				t = substr(line, RSTART + 2, RLENGTH - 3)
				print t
				line = substr(line, RSTART + RLENGTH)
			}
		}' "$f")
done
if ((broken > 0)); then
	echo "docs-check: FAIL ($broken broken of $checked relative links in ${#files[@]} files)"
	exit 1
fi
echo "docs-check: PASS ($checked relative links in ${#files[@]} files)"
