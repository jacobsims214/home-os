#!/usr/bin/env bash
# Asserts the root .dockerignore allowlist covers every COPY a root-context
# Dockerfile makes (#5692).
#
# The root /.dockerignore is a hand-kept allowlist: `*` then `!path`. BuildKit
# reads it from the context root, so a `COPY packages/<x>/` whose path is not
# re-included is ABSENT from the context archive and the build dies at checksum
# time, before compiling anything, on every image that copies it:
#
#   failed to compute cache key: "/packages/notify": not found
#
# It has fired three times. icalx, billcal and billdue were fine only because
# the same commit happened to touch .dockerignore; allergen (#4891 -> 0a31798)
# and notify (#5516 -> #5661) were not, and each cost a release outage. That is
# luck, not a rule, so this asserts the rule instead of hand-keeping the file.
#
# ASSERT, DO NOT GENERATE (#5661's reasoning): generating .dockerignore from the
# Dockerfiles would hide the change inside a file nobody diffs. This fails
# loudly, names the Dockerfile, the COPY line and the exact line to add, and
# leaves the allowlist readable.
#
# Two checks, both static (no Docker daemon, no docker CLI, milliseconds):
#
#   1. every `COPY (packages|apps)/<x>/...` source of a root-context Dockerfile
#      has a matching `^!(packages|apps)/<x>/` line in .dockerignore;
#   2. go.work's `./packages/<x>` module list equals .dockerignore's
#      `!packages/<x>/` set -- go.work is the reliable record of which modules
#      exist, so the two drifting apart is the same defect seen from the other
#      side (a module buildable but not allowlisted, or a stale entry).
#
# `--self-test` runs the checker against fixture trees whose verdicts are known
# (clean, the #5661 notify shape, the #4891 allergen shape, go.work drift, a
# stale allowlist entry, no discoverable Dockerfile) and fails if any verdict is
# wrong. That is the negative control: without it, a broken COPY parser would
# report success while blind, which is the failure class this project keeps
# paying for.
#
# Run: tools/ci/check-dockerignore-allowlist.sh [<repo-root>]   (default: repo root)
#      tools/ci/check-dockerignore-allowlist.sh --self-test
set -uo pipefail

fails=0
fail() { printf 'FAIL: %s\n' "$*" >&2; fails=$((fails + 1)); }
ok()   { printf 'ok: %s\n' "$*"; }
note() { printf 'note: %s\n' "$*"; }

# A GitHub annotation puts the problem on the PR's diff, not only in the log.
annotate() { # <file> <line> <message>
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    printf '::error file=%s,line=%s::%s\n' "$1" "$2" "$3" >&2
  fi
}

# --- root-context Dockerfiles -------------------------------------------------
# Derived from the build definitions, never hardcoded, so an image added to the
# release matrix is covered the moment it exists. release.yml: a matrix entry
# with `context: .`; deploy/docker-compose.yml: `context: ..` (that file lives
# in deploy/, so `..` IS the repo root).
root_context_dockerfiles() { # <repo-root>
  local root="$1"
  {
    awk '
      /^[[:space:]]*-[[:space:]]*service:/ { ctx = ""; next }
      /^[[:space:]]*context:/ {
        v = $0; sub(/^[[:space:]]*context:[[:space:]]*/, "", v); ctx = v; next
      }
      /^[[:space:]]*file:/ {
        v = $0; sub(/^[[:space:]]*file:[[:space:]]*/, "", v)
        if (ctx == ".") print v
        next
      }
    ' "$root/.github/workflows/release.yml" 2>/dev/null

    awk '
      /^[[:space:]]*build:[[:space:]]*$/ { ctx = ""; df = ""; next }
      /^[[:space:]]*context:/ {
        v = $0; sub(/^[[:space:]]*context:[[:space:]]*/, "", v); ctx = v; next
      }
      /^[[:space:]]*dockerfile:/ {
        v = $0; sub(/^[[:space:]]*dockerfile:[[:space:]]*/, "", v); df = v; next
      }
      # A root context, seen from deploy/: ".." (or "../.").
      /^[[:space:]]*(dockerfile|image|restart|target|depends_on|environment):/ {
        if ((ctx == ".." || ctx == "../.") && df != "") { print df; df = "" }
        next
      }
    ' "$root/deploy/docker-compose.yml" 2>/dev/null
  } | sed '/^[[:space:]]*$/d' | sort -u
}

# --- COPY sources -------------------------------------------------------------
# Prints "<line>\t<source>\t<full instruction>" for every source a COPY takes
# from the build context. Continuations are joined (the reported line number is
# the instruction's first line), `--from=` stages are dropped (they are not
# context), and both the shell and the JSON array form are understood.
copy_sources() { # <dockerfile>
  awk '
    {
      line = $0
      while (line ~ /\\[[:space:]]*$/) {
        sub(/\\[[:space:]]*$/, "", line)
        if ((getline more) <= 0) break
        line = line " " more
      }
      raw = line
      sub(/^[[:space:]]+/, "", line)
      if (line == "" || line ~ /^#/) next

      split(line, w, /[[:space:]]+/)
      if (toupper(w[1]) != "COPY") next

      rest = substr(line, length(w[1]) + 1)
      sub(/^[[:space:]]+/, "", rest)

      fromstage = 0
      while (rest ~ /^--/) {
        match(rest, /^--[^[:space:]]+/)
        flag = substr(rest, 1, RLENGTH)
        if (flag ~ /^--from=/) fromstage = 1
        rest = substr(rest, RLENGTH + 1)
        sub(/^[[:space:]]+/, "", rest)
      }
      if (fromstage) next

      if (rest ~ /^\[/) {
        gsub(/[]["]/, "", rest)        # JSON array form: ["a", "b"]
        n = split(rest, parts, /,[[:space:]]*/)
      } else {
        n = split(rest, parts, /[[:space:]]+/)
      }
      if (n < 2) next                  # needs at least source + destination

      for (i = 1; i < n; i++) printf "%d\t%s\t%s\n", NR, parts[i], raw
    }
  ' "$1"
}

# True when .dockerignore has the exact re-include line for this path.
allowlisted() { # <dockerignore> <module-dir>   e.g. packages/notify
  grep -qxF "!$2/" "$1" 2>/dev/null
}

# --- check 1: every context COPY source is allowlisted ------------------------
check_copy_sources() { # <repo-root> <dockerfile>
  local root="$1" df="$2" lineno src raw dir pkg near key
  # A multi-source COPY (packages/x/go.mod packages/x/go.sum) is one missing
  # allowlist line, not two: report each file+line+module once.
  local seen=" "
  while IFS=$'\t' read -r lineno src raw; do
    [ -n "${src:-}" ] || continue

    src="${src#./}"
    # Not a top-level packages/ or apps/ path: relative to some other context,
    # or an in-stage path. Not this file's business.
    case "$src" in
      packages/*|apps/*) ;;
      *) continue ;;
    esac

    # A wildcard or a variable would need every possible expansion allowlisted;
    # say so out loud rather than pretend it was checked.
    case "$src" in
      *'*'*|*'$'*)
        note "$df:$lineno COPY $src -- wildcard source, not checked (unfold it or extend this check)"
        continue
        ;;
    esac

    case "$src" in
      packages|apps)
        fail "$df:$lineno copies the whole '$src/' tree; the allowlist cannot express that and this check cannot verify it -- copy individual modules instead"
        continue
        ;;
    esac

    dir="${src%%/*}"                 # packages | apps
    pkg="$(printf '%s' "$src" | cut -d/ -f2)"
    if [ -z "$pkg" ]; then
      fail "$df:$lineno COPY $src -- cannot tell which module it needs allowlisted"
      continue
    fi

    allowlisted "$root/.dockerignore" "$dir/$pkg" && continue

    key=" $lineno/$dir/$pkg "
    case "$seen" in *"$key"*) continue ;; esac
    seen="$seen$key"

    near="$(grep -n "^!$dir/$pkg" "$root/.dockerignore" 2>/dev/null | head -1)"
    fail "$df:$lineno copies $dir/$pkg from the root context, but .dockerignore has no '!$dir/$pkg/' line"
    printf '      %s\n' "$raw" >&2
    printf '      BuildKit fails on every root-context image that copies it:\n' >&2
    printf '        failed to compute cache key: "/%s/%s": not found\n' "$dir" "$pkg" >&2
    printf '      Add to .dockerignore:  !%s/%s/\n' "$dir" "$pkg" >&2
    if [ -n "$near" ]; then
      printf '      (found .dockerignore:%s, which does not match exactly -- it needs the trailing slash)\n' "$near" >&2
    fi
    annotate "$df" "$lineno" "COPY $dir/$pkg/ is not re-included in .dockerignore; add '!$dir/$pkg/' or the build fails with \"/$dir/$pkg\": not found"
  done < <(copy_sources "$root/$df")
}

# --- check 2: go.work packages == .dockerignore packages ----------------------
check_go_work() { # <repo-root>
  local root="$1" gw di only_gw only_di p
  gw="$(awk '/^[[:space:]]*\.\/packages\//{
                l = $0
                sub(/^[[:space:]]*\.\/packages\//, "", l)
                sub(/[[:space:]].*$/, "", l)
                sub(/\/$/, "", l)
                if (l != "") print l
              }' "$root/go.work" 2>/dev/null | sort -u)"
  di="$(awk '/^!packages\//{ l = $0; sub(/^!packages\//, "", l); sub(/\/$/, "", l); if (l != "" && l !~ /\//) print l }' \
        "$root/.dockerignore" 2>/dev/null | sort -u)"

  only_gw="$(comm -23 <(printf '%s\n' "$gw") <(printf '%s\n' "$di"))"
  only_di="$(comm -13 <(printf '%s\n' "$gw") <(printf '%s\n' "$di"))"

  if [ -z "$only_gw" ] && [ -z "$only_di" ]; then
    ok "go.work's ./packages set equals .dockerignore's !packages set ($(printf '%s' "$gw" | tr '\n' ' ' | sed 's/ *$//'))"
    return
  fi

  for p in $only_gw; do
    fail "go.work lists ./packages/$p but .dockerignore has no '!packages/$p/' -- a module the build needs is excluded from the root context"
    printf '      Add to .dockerignore:  !packages/%s/\n' "$p" >&2
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
      printf '::error::go.work lists ./packages/%s but .dockerignore does not allowlist !packages/%s/\n' "$p" "$p" >&2
    fi
  done
  for p in $only_di; do
    fail ".dockerignore allows '!packages/$p/' but go.work does not list ./packages/$p -- a stale allowlist entry (go.work is the record of which modules exist)"
    printf '      Remove from .dockerignore:  !packages/%s/\n' "$p" >&2
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
      printf '::error::.dockerignore allows !packages/%s/ but go.work does not list ./packages/%s\n' "$p" "$p" >&2
    fi
  done
}

# --- the unbuilt-Dockerfile sweep ---------------------------------------------
# Discovery above follows release.yml and the compose file. A Dockerfile that is
# in neither, but copies packages/ or apps/, is a build path nothing watches --
# exactly where this defect hides. Say so instead of passing over it.
check_unbuilt_dockerfiles() { # <repo-root> <known root-context list>
  local root="$1" known="$2" df lineno src raw
  while IFS= read -r df; do
    [ -n "$df" ] || continue
    case "$df" in */Dockerfile|*/Dockerfile.*) ;; *) continue ;; esac
    printf '%s\n' "$known" | grep -qxF "$df" && continue

    while IFS=$'\t' read -r lineno src raw; do
      src="${src#./}"
      case "$src" in
        packages/*|apps/*)
          case "$src" in
            *'*'*|*'$'*) continue ;;
          esac
          fail "$df:$lineno copies $src but $df is not a root-context image in release.yml or deploy/docker-compose.yml -- this check has no idea whether its context is right"
          printf '      Add it to the release matrix or the compose file so it gets checked, or teach this check about it.\n' >&2
          ;;
      esac
    done < <(copy_sources "$root/$df")
  done < <(cd "$root" && find . \
              \( -name .git -o -name .worktrees -o -name node_modules -o -name .next \
                 -o -name vendor -o -name dist -o -name coverage \) -prune -o \
              -name 'Dockerfile*' -type f -print 2>/dev/null | sed 's|^\./||' | sort)
}

# --- the check ---------------------------------------------------------------
check_repo() { # <repo-root>
  local root="$1" dfs df before=$fails
  local n=0

  [ -f "$root/.dockerignore" ] || { fail "$root/.dockerignore is missing -- the root-context builds have no allowlist at all"; return; }
  [ -f "$root/go.work" ]       || { fail "$root/go.work is missing"; }

  dfs="$(root_context_dockerfiles "$root")"
  if [ -z "$dfs" ]; then
    # Fail closed: "no Dockerfiles found" is a broken check, not a clean tree.
    fail "found no root-context Dockerfiles in release.yml or deploy/docker-compose.yml -- this check is blind"
    return
  fi

  while IFS= read -r df; do
    [ -n "$df" ] || continue
    if [ ! -f "$root/$df" ]; then
      fail "$df is in the release matrix but does not exist"
      continue
    fi
    check_copy_sources "$root" "$df"
    n=$((n + 1))
  done <<< "$dfs"

  [ -f "$root/go.work" ] && check_go_work "$root"
  check_unbuilt_dockerfiles "$root" "$dfs"

  if [ "$fails" -eq "$before" ]; then
    ok "$n root-context Dockerfiles, every COPY source allowlisted in .dockerignore"
  fi
}

# --- self-test: fixtures with known verdicts ---------------------------------
fixture() { # <dir> <go.work body> <dockerignore !lines> <Dockerfile body>
  local d="$1"
  mkdir -p "$d/.github/workflows" "$d/deploy" "$d/apps/api" "$d/apps/ui" "$d/packages/notify" "$d/packages/db"
  cat > "$d/.github/workflows/release.yml" <<'YML'
    strategy:
      matrix:
        include:
          - service: api
            context: .
            file: apps/api/Dockerfile
          - service: ui
            context: apps/ui
            file: apps/ui/Dockerfile
YML
  cat > "$d/deploy/docker-compose.yml" <<'YML'
services:
  migrations:
    build:
      context: ../packages/db
      dockerfile: Dockerfile
YML
  printf '%s\n' "$2" > "$d/go.work"
  { echo '*'; printf '%s\n' "$3"; } > "$d/.dockerignore"
  printf '%s\n' "$4" > "$d/apps/api/Dockerfile"
  printf 'FROM busybox\nCOPY . .\n' > "$d/packages/db/Dockerfile"
  printf 'FROM node\nCOPY . .\n' > "$d/apps/ui/Dockerfile"
}

self_test() {
  local tmp; tmp="$(mktemp -d)"
  local self="$0" d out rc

  d="$tmp/clean"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n\t./packages/notify\n)' \
    $'!packages/allergen/\n!packages/notify/\n!apps/api/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/allergen/go.mod /src/packages/allergen/\nCOPY packages/notify/ /src/packages/notify/\nCOPY apps/api/ ./\nFROM distroless\nCOPY --from=builder /x /x'
  out="$("$self" "$d" 2>&1)"; rc=$?
  [ "$rc" -eq 0 ] && ok "self-test: clean tree passes" \
    || { fail "self-test: clean tree should pass but exited $rc"; printf '%s\n' "$out" | sed 's/^/      /' >&2; }

  d="$tmp/notify"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n\t./packages/notify\n)' \
    $'!packages/allergen/\n!apps/api/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/notify/go.mod /src/packages/notify/'
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "self-test: the #5661 shape (COPY packages/notify/, no !packages/notify/) PASSED -- the check is blind"
  elif ! printf '%s' "$out" | grep -q 'apps/api/Dockerfile:2 copies packages/notify' \
       || ! printf '%s' "$out" | grep -q 'Add to .dockerignore:  !packages/notify/'; then
    fail "self-test: the #5661 shape failed but without naming the Dockerfile, the COPY line and the line to add"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  elif ! printf '%s' "$out" | grep -q 'not found'; then
    fail "self-test: the #5661 shape did not explain the BuildKit consequence"
  else
    ok "self-test: the #5661 shape fails, naming the Dockerfile, the COPY line, the line to add and the not-found consequence"
  fi

  d="$tmp/allergen"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n)' \
    $'!packages/allergen/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/allergen/ /src/packages/allergen/\nCOPY packages/other/ /src/packages/other/'
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] || ! printf '%s' "$out" | grep -q '!packages/other/'; then
    fail "self-test: the #4891 shape (COPY a package the allowlist omits) did not fail naming the line to add"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  else
    ok "self-test: the #4891 shape fails, naming '!packages/other/'"
  fi

  d="$tmp/gowork"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n\t./packages/notify\n)' \
    $'!packages/allergen/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/allergen/ /src/packages/allergen/'
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] || ! printf '%s' "$out" | grep -q 'go.work lists ./packages/notify'; then
    fail "self-test: go.work listing ./packages/notify with no '!packages/notify/' did not fail"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  else
    ok "self-test: go.work drift fails, naming ./packages/notify"
  fi

  d="$tmp/stale"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n)' \
    $'!packages/allergen/\n!packages/gone/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/allergen/ /src/packages/allergen/'
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] || ! printf '%s' "$out" | grep -q 'stale allowlist entry'; then
    fail "self-test: '!packages/gone/' with no ./packages/gone in go.work did not fail"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  else
    ok "self-test: a stale '!packages/gone/' entry fails"
  fi

  # Fail closed: a tree with no build definitions must not look clean.
  d="$tmp/blind"; mkdir -p "$d/.github/workflows" "$d/deploy"
  : > "$d/.github/workflows/release.yml"
  : > "$d/deploy/docker-compose.yml"
  printf '*\n' > "$d/.dockerignore"
  printf 'go 1.25.5\n' > "$d/go.work"
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "self-test: a tree with no root-context Dockerfile PASSED -- the check reports success while blind"
  else
    ok "self-test: no discoverable Dockerfile fails closed"
  fi

  # Fail closed: an unbuilt Dockerfile that copies packages/ is a blind spot.
  d="$tmp/unbuilt"; fixture "$d" \
    $'go 1.25.5\n\nuse (\n\t./packages/allergen\n)' \
    $'!packages/allergen/\n!apps/api/' \
    $'FROM golang:1.25-alpine AS builder\nCOPY packages/allergen/ /src/packages/allergen/'
  mkdir -p "$d/tools/scratch"
  printf 'FROM golang:1.25-alpine\nCOPY packages/allergen/ /x\n' > "$d/tools/scratch/Dockerfile"
  out="$("$self" "$d" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] || ! printf '%s' "$out" | grep -q 'tools/scratch/Dockerfile'; then
    fail "self-test: a Dockerfile outside the release matrix that copies packages/ did not fail"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  else
    ok "self-test: a Dockerfile outside the release matrix that copies packages/ fails, naming it"
  fi

  rm -rf "$tmp"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  echo
  if [ "$fails" -ne 0 ]; then
    echo "$fails self-test check(s) failed" >&2
    exit 1
  fi
  echo "all dockerignore-allowlist self-tests passed"
  exit 0
fi

ROOT="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
check_repo "$ROOT"

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails problem(s): the root .dockerignore allowlist does not cover the root-context builds (#5692)." >&2
  echo "Adding an image or a module means adding its '!<dir>/<name>/' line." >&2
  exit 1
fi
echo "dockerignore allowlist matches every root-context Dockerfile COPY"
