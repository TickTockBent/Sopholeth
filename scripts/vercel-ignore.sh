#!/bin/sh
# Run from the Vercel project's site directory. Vercel uses 0 = skip,
# 1 = build. If a reliable comparison cannot be made, allow the build.

build() {
    printf '%s\n' "Build: $1"
    exit 1
}

export GIT_TERMINAL_PROMPT=0
site_prefix=$(git rev-parse --show-prefix) || build 'not a Git checkout'
case "$site_prefix" in
    sites/sopholeth.com/|sites/sopholeth.io/|sites/sopholeth.dev/|sites/soph.stream/) ;;
    *) build 'expected a site root as the working directory' ;;
esac

baseline=${VERCEL_GIT_PREVIOUS_SHA:-}
if [ -n "$baseline" ]; then
    # Only accept a commit hash, not arbitrary Git options or revision syntax.
    case "$baseline" in *[!0-9a-fA-F]*) build 'invalid previous deployment SHA' ;; esac
    if ! git cat-file -e "$baseline^{commit}" 2>/dev/null; then
        git fetch --quiet --no-tags --depth=1 origin "$baseline" || build 'previous deployment unavailable'
    fi
elif [ "${VERCEL_ENV:-}" = preview ] && [ -n "${VERCEL_GIT_COMMIT_REF:-}" ] && [ "$VERCEL_GIT_COMMIT_REF" != main ]; then
    # A new preview branch has no prior deployment. Compare its entire
    # branch diff, not just HEAD^, which misses earlier commits in a push.
    git fetch --quiet --no-tags --depth=100 origin main || build 'main unavailable'
    baseline=$(git merge-base HEAD FETCH_HEAD) || build 'branch history unavailable'
else
    build 'first production deployment or no Git deployment context'
fi

# The pathspec is relative to the site root, so other sites and repo docs
# cannot trigger this project. Renames and deletions also count as changes.
if git diff --quiet "$baseline" HEAD -- .; then
    printf '%s\n' "Skip: $site_prefix has no changes since $baseline"
    exit 0
fi
build 'site changed or Git comparison failed'
