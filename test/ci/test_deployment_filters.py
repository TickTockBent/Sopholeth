"""Exercise real Vercel ignore commands and the workflow path contract."""

import ast
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SITES = ("sopholeth.com", "sopholeth.io", "sopholeth.dev", "soph.stream")


class VercelIgnoreTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name) / "repo"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "CI fixture")
        self.git("config", "user.email", "ci@example.invalid")
        (self.repo / "scripts").mkdir()
        shutil.copy(ROOT / "scripts/vercel-ignore.sh", self.repo / "scripts")
        for site in SITES:
            directory = self.repo / "sites" / site
            directory.mkdir(parents=True)
            shutil.copy(ROOT / "sites" / site / "vercel.json", directory)
            (directory / "index.html").write_text("initial site\n")
        self.base = self.commit()

    def git(self, *args, repo=None):
        return subprocess.check_output(
            ["git", *args], cwd=repo or self.repo, stderr=subprocess.PIPE, text=True
        ).strip()

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-m", "fixture", "--quiet")
        return self.git("rev-parse", "HEAD")

    def change(self, path, text="changed\n"):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)
        return self.commit()

    def decisions(self, previous=None, branch="feature", environment="preview", repo=None):
        checkout = repo or self.repo
        env = {k: v for k, v in os.environ.items() if not k.startswith("VERCEL_")}
        env.update(VERCEL_ENV=environment, VERCEL_GIT_COMMIT_REF=branch)
        if previous is not None:
            env["VERCEL_GIT_PREVIOUS_SHA"] = previous
        decisions = {}
        for site in SITES:
            directory = checkout / "sites" / site
            command = json.loads((directory / "vercel.json").read_text())["ignoreCommand"]
            result = subprocess.run(
                ["sh", "-c", command], cwd=directory, env=env,
                capture_output=True, text=True, timeout=15,
            )
            self.assertIn(result.returncode, (0, 1), result.stderr)
            decisions[site] = result.returncode
        return decisions

    def test_docs_and_other_site_are_skipped(self):
        self.change("docs/plan.md")
        self.assertEqual(self.decisions(self.base), dict.fromkeys(SITES, 0))
        for site in SITES:
            before = self.git("rev-parse", "HEAD")
            self.change(f"sites/{site}/index.html")
            with self.subTest(site=site):
                self.assertEqual(self.decisions(before), {s: int(s == site) for s in SITES})

    def test_new_preview_compares_all_commits(self):
        self.git("remote", "add", "origin", self.repo.as_uri())
        self.git("checkout", "-b", "feature")
        self.change("sites/soph.stream/index.html")
        self.change("docs/last-commit.md")
        self.assertEqual(self.decisions(), {s: int(s == "soph.stream") for s in SITES})

    def test_new_docs_only_preview_is_skipped(self):
        self.git("remote", "add", "origin", self.repo.as_uri())
        self.git("checkout", "-b", "feature")
        self.change("docs/first.md")
        self.change("docs/second.md")
        self.assertEqual(self.decisions(), dict.fromkeys(SITES, 0))

    def test_previous_deployment_catches_earlier_changes(self):
        self.change("sites/sopholeth.io/index.html")
        self.change("docs/last-commit.md")
        self.assertEqual(self.decisions(self.base), {s: int(s == "sopholeth.io") for s in SITES})

    def test_helper_does_not_need_files_outside_site_root(self):
        self.change("docs/only.md")
        shutil.rmtree(self.repo / "scripts")
        self.assertEqual(self.decisions(self.base), dict.fromkeys(SITES, 0))

    def test_vercel_cleanup_preserves_git_history(self):
        self.change("docs/only.md")
        for site in SITES:
            with self.subTest(site=site):
                checkout = Path(self.temp.name) / site
                shutil.copytree(self.repo, checkout)
                paths = [str(p.relative_to(checkout)) for p in checkout.rglob("*") if p.is_file()]
                # Vercel applies the site's .vercelignore to the clone before
                # running ignoreCommand. Evaluate its gitignore-style rules
                # with Git, including metadata such as .git/HEAD and objects.
                ignore_file = ROOT / "sites" / site / ".vercelignore"
                ignored = subprocess.run(
                    ["git", "-c", f"core.excludesFile={ignore_file}",
                     "check-ignore", "--no-index", "--stdin"],
                    cwd=checkout, input="\n".join(paths) + "\n",
                    capture_output=True, text=True, timeout=15,
                )
                self.assertIn(ignored.returncode, (0, 1), ignored.stderr)
                for path in ignored.stdout.splitlines():
                    (checkout / path).unlink()
                self.assertEqual(self.decisions(self.base, repo=checkout), dict.fromkeys(SITES, 0))

    def test_rename_between_sites_deploys_both(self):
        self.git("mv", "sites/sopholeth.com/index.html", "sites/sopholeth.dev/moved.html")
        self.commit()
        self.assertEqual(self.decisions(self.base), {s: int(s in ("sopholeth.com", "sopholeth.dev")) for s in SITES})

    def test_first_production_and_unknown_history_build(self):
        self.assertEqual(self.decisions(branch="main", environment="production"), dict.fromkeys(SITES, 1))
        self.assertEqual(self.decisions("f" * 40), dict.fromkeys(SITES, 1))
        self.assertEqual(self.decisions("--invalid-option"), dict.fromkeys(SITES, 1))
        self.assertEqual(self.decisions(), dict.fromkeys(SITES, 1))

    def test_missing_old_commit_is_fetched_from_shallow_clone(self):
        self.change("sites/sopholeth.com/index.html")
        self.change("docs/last-commit.md")
        checkout = Path(self.temp.name) / "shallow"
        self.git("clone", "--quiet", "--depth=1", self.repo.as_uri(), str(checkout))
        self.assertEqual(self.git("rev-list", "--count", "HEAD", repo=checkout), "1")
        self.assertEqual(self.decisions(self.base, repo=checkout), {s: int(s == "sopholeth.com") for s in SITES})


def event_paths(workflow, event):
    """Read the quoted path lists we maintain; unexpected formatting fails."""
    text = (ROOT / ".github/workflows" / workflow).read_text()
    event_block = re.search(rf"^  {event}:\n(.*?)(?=^  \w|^\S|\Z)", text, re.M | re.S)
    if event_block is None:
        raise AssertionError(f"Missing {event} in {workflow}")
    paths = re.search(r"^    paths:\n((?:      - .+\n)+)", event_block[1], re.M)
    if paths is None:
        raise AssertionError(f"Missing path filter in {workflow} {event}")
    return [ast.literal_eval(line.strip()[2:]) for line in paths[1].splitlines()]


def matches(paths, filename):
    included = False
    for pattern in paths:
        exclude = pattern.startswith("!")
        pattern = pattern.removeprefix("!")
        regex = re.escape(pattern).replace(r"\*\*/", "(?:.*/)?").replace(r"\*\*", ".*").replace(r"\*", "[^/]*")
        if re.fullmatch(regex, filename):
            included = not exclude
    return included


class WorkflowFilterTests(unittest.TestCase):
    def test_workload_matrix(self):
        # Expected Go-test / node-image behavior, independent of filter spelling.
        cases = {
            "README.md": (False, False),
            "docs/public-network-plan.md": (False, False),
            "sites/sopholeth.com/index.html": (False, False),
            "sites/sopholeth.io/index.html": (False, False),
            "sites/sopholeth.dev/index.html": (False, False),
            "sites/soph.stream/vercel.json": (False, False),
            "sites/soph.stream/404.html": (False, False),
            "sites/soph.stream/viewer.js": (True, False),
            "sites/soph.stream/config.json": (True, False),
            "sites/stream.go": (True, False),
            "cmd/soph/serve.go": (True, False),
            "test/burnin/legacy-omega/main.go": (True, False),
            "cmd/dashboard/web/index.html": (True, False),
            "internal/client/client.go": (True, False),
            "internal/dashboard/poller.go": (True, False),
            "cmd/server/main.go": (True, True),
            "cmd/server/handlers_test.go": (True, False),
            "internal/trust/omega.go": (True, True),
            "internal/gossip/protocol_test.go": (True, False),
            "internal/storage/testdata/fixture.json": (True, False),
            "internal/storage/README.md": (False, False),
            "internal/newpackage/runtime.go": (True, True),
            "go.mod": (True, True),
            "go.sum": (True, True),
            "go.work": (True, True),
            "Makefile": (True, False),
            "Dockerfile": (False, True),
            ".dockerignore": (False, True),
            ".github/workflows/test.yml": (True, False),
            ".github/workflows/docker-build.yml": (False, True),
            "test/burnin/Dockerfile.go-node": (False, False),
            "internal/trust/bootstrap/client.go": (True, True),
            "internal/trust/bootstrap/client_test.go": (True, False),
            "test/omega-tuf/README.md": (False, False),
        }
        for event in ("push", "pull_request"):
            go_paths = event_paths("test.yml", event)
            docker_paths = event_paths("docker-build.yml", event)
            for path, expected in cases.items():
                with self.subTest(event=event, path=path):
                    self.assertEqual((matches(go_paths, path), matches(docker_paths, path)), expected)



if __name__ == "__main__":
    unittest.main()
