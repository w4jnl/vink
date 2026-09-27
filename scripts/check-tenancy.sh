#!/bin/sh
# Fails when a query file selects from a project-scoped table without
# filtering on project_id, or from an org-scoped table without org_id.
# Every query is expected to carry the scope predicate in its WHERE clause.
set -eu

dir="$(dirname "$0")/../internal/db/queries"
[ -d "$dir" ] || exit 0

project_tables="monitors observations bodies events incidents channels routes deliveries api_keys maintenance status_pages"
org_tables="projects memberships agents"
status=0

check() {
  table="$1"; column="$2"
  for f in "$dir"/*.sql; do
    # one statement per "-- name:" block; a block is the text up to the next blank line
    awk -v tbl="$table" -v col="$column" -v file="$f" '
      /^-- name:/ { if (block != "") inspect(); block=""; name=$3 }
      { block = block "\n" $0 }
      END { if (block != "") inspect() }
      function inspect() {
        lower = tolower(block)
        if (lower ~ ("(from|update|into|join)[[:space:]]+" tbl "([[:space:]]|$|\\()") && lower !~ col) {
          printf "%s: %s touches %s without %s\n", file, name, tbl, col
          exit_code = 1
        }
      }
      END { exit exit_code }
    ' "$f" || status=1
  done
}

for t in $project_tables; do check "$t" "project_id"; done
for t in $org_tables; do check "$t" "org_id"; done

if [ "$status" -ne 0 ]; then
  echo "tenancy gate failed: add the scope predicate to the queries above" >&2
  exit 1
fi
echo "tenancy gate ok"
