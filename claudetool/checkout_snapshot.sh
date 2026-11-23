# Print the worktree identity, content fingerprint and HEAD for a Bash job.
set -eo pipefail
root=$(git -C "$1" rev-parse --show-toplevel 2>/dev/null) || exit 0
cd -- "$root"
branch=$(git symbolic-ref --short -q HEAD || :)
commit=$(git rev-parse --verify HEAD 2>/dev/null || :)
subject=
if [[ -n $commit ]]; then subject=$(git show -s --format=%s HEAD); fi

fingerprint=$(
  {
    git diff --no-ext-diff --no-textconv --binary --no-color --
    git diff --cached --raw --no-abbrev -z --
    git ls-files --others --exclude-standard -z |
      while IFS= read -r -d '' path; do
        printf '%s\0' "$path"
        if [[ -L $path ]]; then readlink "$path"
        elif [[ -f $path ]]; then
          if [[ -x $path ]]; then printf x; fi
          git hash-object --no-filters -- "$path"
        fi
      done
  } | git hash-object --stdin
)
printf '%s\0' "$root" "$fingerprint" "$branch" "$commit" "$subject"
