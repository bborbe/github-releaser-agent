#!/usr/bin/env python3
# Copyright (c) 2026 Benjamin Borbe All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
"""Report repos that carry a Dockerfile but never opted into dockerBuild.

Reads the recorded release reviews (the `## Review` JSON block in the
github-releaser agent's task records) and lists every repo that closed a
release with `image_opt_in: none` -- or, on a record written before that
field existed, `image_expected: false` with no `short-circuit` note -- while
carrying a `Dockerfile` on its default branch.

The point of the report is the coverage gap, not a failure: a repo with a
Dockerfile and no `dockerBuild` opt-in publishes no image, and nothing in the
release pipeline says so. This script is the downstream consumer that says so.

The reachable population is bounded by when `image_expected` started being
recorded (v0.8.x), so the output reconciles: repos with a recorded review plus
repos with a Dockerfile and no recorded review must add up to the Dockerfile
population.

Usage:
    hack/report-image-opt-in.py --tasks-dir DIR [--dockerfile-repos FILE]
                                [--github-owner bborbe] [--json]

--tasks-dir is the directory holding the github-releaser task records (for
example the `tasks/` directory of the agent vault).
--dockerfile-repos points at a file of `owner/repo` lines, one per repo that
carries a Dockerfile. When omitted the list is derived with `gh api
search/code`, which needs an authenticated `gh`.
"""

import argparse
import json
import os
import re
import subprocess
import sys

FRONTMATTER_RE = re.compile(r"\A---\n(.*?)\n---\n", re.DOTALL)
REVIEW_RE = re.compile(r"^## Review\s*$", re.MULTILINE)
JSON_BLOCK_RE = re.compile(r"```json\n(.*?)\n```", re.DOTALL)
SHORT_CIRCUIT = "short-circuit"


def parse_frontmatter(text):
    match = FRONTMATTER_RE.match(text)
    if not match:
        return {}
    fields = {}
    for line in match.group(1).splitlines():
        if ":" not in line or line.startswith((" ", "\t", "-")):
            continue
        key, _, value = line.partition(":")
        fields[key.strip()] = value.strip().strip('"').strip("'")
    return fields


def parse_review(text):
    """Return the parsed `## Review` JSON block, or None."""
    marker = REVIEW_RE.search(text)
    if not marker:
        return None
    block = JSON_BLOCK_RE.search(text, marker.end())
    if not block:
        return None
    try:
        return json.loads(block.group(1))
    except json.JSONDecodeError:
        return None


def opt_in_cause(review):
    """Classify why the image check did or did not probe.

    Post-change records carry `image_opt_in` directly. Pre-change records only
    carry `image_expected`, so the short-circuit path is recognised by its
    note. A record written before either field existed carries neither: it says
    nothing about the opt-in and is reported as `unrecorded`, never guessed
    into `none`.
    """
    checks = review.get("checks") or {}
    recorded = checks.get("image_opt_in")
    if recorded:
        return recorded
    if "image_expected" not in checks:
        return "unrecorded"
    if checks["image_expected"]:
        return "auto_build"
    notes = review.get("notes") or ""
    if SHORT_CIRCUIT in notes:
        return "skipped"
    return "unknown-legacy"


def version_key(tag):
    """Order tags so the newest recorded release wins a per-repo tie."""
    numbers = [int(part) for part in re.findall(r"\d+", tag)]
    return numbers or [0]


def read_records(tasks_dir):
    for name in sorted(os.listdir(tasks_dir)):
        if not name.endswith(".md") or not name.startswith("Release "):
            continue
        path = os.path.join(tasks_dir, name)
        try:
            with open(path, encoding="utf-8") as handle:
                text = handle.read()
        except OSError:
            continue
        fields = parse_frontmatter(text)
        repo = fields.get("repo", "")
        if not repo:
            continue
        review = parse_review(text)
        if review is None:
            continue
        yield {
            "file": name,
            "repo": repo,
            "tag": fields.get("current_version", ""),
            "opt_in": opt_in_cause(review),
        }


def dockerfile_repos_from_github(owner):
    repos = set()
    for page in range(1, 11):
        query = f"filename:Dockerfile+user:{owner}&per_page=100&page={page}"
        result = subprocess.run(
            ["gh", "api", f"search/code?q={query}", "--jq", ".items[].repository.full_name"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            sys.exit(f"gh api failed: {result.stderr.strip()}")
        lines = [line for line in result.stdout.splitlines() if line]
        if not lines:
            break
        repos.update(lines)
    return repos


def dockerfile_repos_from_file(path):
    with open(path, encoding="utf-8") as handle:
        return {line.strip() for line in handle if line.strip()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--tasks-dir",
        required=True,
        help="directory holding the github-releaser task records",
    )
    parser.add_argument("--dockerfile-repos", default=None)
    parser.add_argument("--github-owner", default="bborbe")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    if not os.path.isdir(args.tasks_dir):
        sys.exit(f"tasks dir not found: {args.tasks_dir}")

    if args.dockerfile_repos:
        dockerfile_repos = dockerfile_repos_from_file(args.dockerfile_repos)
    else:
        dockerfile_repos = dockerfile_repos_from_github(args.github_owner)

    records = list(read_records(args.tasks_dir))
    seen = {}
    for record in records:
        if record["repo"] not in dockerfile_repos:
            continue
        current = seen.get(record["repo"])
        if current is None or version_key(record["tag"]) > version_key(current["tag"]):
            seen[record["repo"]] = record

    # Every recorded cause must land in exactly one bucket, or the
    # reconciliation below is a claim rather than a check. A cause added to
    # the agent without being added here would silently vanish from the count.
    buckets = {
        "no_opt_in": [
            row for row in seen.values() if row["opt_in"] in ("none", "unknown-legacy")
        ],
        "unrecorded": [row for row in seen.values() if row["opt_in"] == "unrecorded"],
        "opted_in": [row for row in seen.values() if row["opt_in"] == "auto_build"],
        "other_cause": [
            row
            for row in seen.values()
            if row["opt_in"] in ("skipped", "empty_destinations", "no_config")
        ],
    }
    bucketed = {row["repo"] for rows in buckets.values() for row in rows}
    unclassified = sorted(set(seen) - bucketed)
    if unclassified:
        sys.exit(f"unclassified opt-in causes: {', '.join(unclassified)}")

    rows = sorted(buckets["no_opt_in"], key=lambda row: row["repo"])
    with_review = {row["repo"] for row in rows}
    unrecorded = sorted(row["repo"] for row in buckets["unrecorded"])
    opted_in = sorted(row["repo"] for row in buckets["opted_in"])
    other_cause = sorted(row["repo"] for row in buckets["other_cause"])
    unreviewed = sorted(dockerfile_repos - set(seen))

    if args.json:
        json.dump(
            {
                "rows": rows,
                "dockerfile_repos": len(dockerfile_repos),
                "with_review": len(with_review),
                "unrecorded": len(unrecorded),
                "unrecorded_repos": unrecorded,
                "opted_in": len(opted_in),
                "opted_in_repos": opted_in,
                "other_cause": len(other_cause),
                "other_cause_repos": other_cause,
                "without_review": len(unreviewed),
                "without_review_repos": unreviewed,
            },
            sys.stdout,
            indent=2,
        )
        sys.stdout.write("\n")
        return

    print(f"Dockerfile repos: {len(dockerfile_repos)}")
    print(f"Recorded review says no opt-in: {len(with_review)}")
    print(f"Review recorded but before the image field: {len(unrecorded)}")
    print(f"No recorded review at all: {len(unreviewed)}")
    print(f"Recorded review says opted in: {len(opted_in)}")
    print(f"Recorded review carries another cause: {len(other_cause)}")
    total = (
        len(with_review) + len(unrecorded) + len(unreviewed)
        + len(opted_in) + len(other_cause)
    )
    print(f"Reconciles to: {total} (must equal {len(dockerfile_repos)})")
    if total != len(dockerfile_repos):
        sys.exit("reconciliation failed — a bucket is missing")
    print()
    for row in rows:
        print(f"  {row['repo']:<45} {row['tag']:<12} {row['opt_in']}  ({row['file']})")



if __name__ == "__main__":
    main()
