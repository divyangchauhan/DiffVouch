from __future__ import annotations

import argparse
import getpass
import subprocess
import sys
from pathlib import Path

from diffvouch import __version__
from diffvouch.config import load_global_config, save_global_config
from diffvouch.credentials import SecretReference, delete_secret, store_secret
from diffvouch.errors import ArgumentError, DiffVouchError, GitHubError
from diffvouch.git import GitError, repository_root, run_git
from diffvouch.github_app import (
    GitHubAPI,
    configure_app,
    load_app,
    open_creation_page,
    publish_review,
    remove_app,
    resolve_pull_request,
)
from diffvouch.render import render_json, render_terminal
from diffvouch.review import perform_review


def _add_review_parser(subparsers: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    parser = subparsers.add_parser("review", help="review a Git diff or pull request")
    parser.add_argument("--provider", choices=["codex", "claude"], required=True)
    parser.add_argument("--transport", choices=["cli", "api"])
    parser.add_argument("--model")
    parser.add_argument("--effort", choices=["low", "medium", "high", "xhigh", "max"])
    parser.add_argument("--base", help="local Git ref to compare against")
    parser.add_argument("--pr", type=int, help="GitHub pull request number")
    parser.add_argument("--repo", help="GitHub owner/name override")
    parser.add_argument("--github-host", help="GitHub hostname for --repo or Enterprise Server")
    parser.add_argument("--committed-only", action="store_true")
    parser.add_argument("--staged-only", action="store_true")
    parser.add_argument("--exclude", action="append", default=[])
    parser.add_argument("--format", choices=["terminal", "json"], default="terminal")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--publish", action="store_true")
    parser.add_argument("--fail-below", type=float)
    parser.add_argument("--fail-on-severity", choices=["critical", "high", "medium", "low"])
    parser.add_argument("--max-diff-bytes", type=int)
    parser.add_argument("--config")
    parser.add_argument("--no-color", action="store_true")
    parser.add_argument("--verbose", action="store_true")
    parser.set_defaults(handler=_command_review)


def _add_auth_parser(subparsers: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    auth = subparsers.add_parser("auth", help="manage AI provider authentication")
    commands = auth.add_subparsers(dest="auth_command", required=True)
    login = commands.add_parser("login", help="sign in with an existing provider subscription")
    login.add_argument("provider", choices=["openai", "codex", "claude"])
    login.add_argument("--device", action="store_true", help="use Codex device-code login")
    login.set_defaults(handler=_command_auth_login)

    status = commands.add_parser("status", help="show provider authentication status")
    status.add_argument(
        "provider", choices=["openai", "codex", "claude", "all"], default="all", nargs="?"
    )
    status.set_defaults(handler=_command_auth_status)

    set_key = commands.add_parser("set-key", help="securely store a provider API key")
    set_key.add_argument("provider", choices=["openai", "anthropic"])
    set_key.add_argument("--stdin", action="store_true", help="read the key from stdin")
    set_key.add_argument("--storage", choices=["auto", "keyring", "file"], default="auto")
    set_key.set_defaults(handler=_command_set_key)

    remove_key = commands.add_parser("remove-key", help="remove a stored API key")
    remove_key.add_argument("provider", choices=["openai", "anthropic"])
    remove_key.set_defaults(handler=_command_remove_key)


def _add_github_parser(subparsers: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    github = subparsers.add_parser("github", help="manage GitHub publication")
    github_commands = github.add_subparsers(dest="github_command", required=True)
    app = github_commands.add_parser("app", help="manage a user-owned GitHub App")
    commands = app.add_subparsers(dest="app_command", required=True)

    create = commands.add_parser("create", help="open GitHub's App creation page")
    create.add_argument("--host", default="github.com")
    create.add_argument("--owner", help="organization that should own the private app")
    create.add_argument("--no-browser", action="store_true")
    create.set_defaults(handler=_command_github_create)

    configure = commands.add_parser("configure", help="store and validate GitHub App credentials")
    configure.add_argument("--app-id")
    configure.add_argument("--slug")
    configure.add_argument("--private-key", type=Path)
    configure.add_argument("--host", default="github.com")
    configure.add_argument("--api-base-url")
    configure.add_argument("--web-base-url")
    configure.add_argument("--api-version")
    configure.add_argument("--storage", choices=["auto", "keyring", "file"], default="auto")
    configure.set_defaults(handler=_command_github_configure)

    status = commands.add_parser(
        "status", help="validate the app and optional repository installation"
    )
    status.add_argument("--host", default="github.com")
    status.add_argument("--repo", help="owner/name repository to validate")
    status.set_defaults(handler=_command_github_status)

    remove = commands.add_parser("remove", help="remove locally stored GitHub App credentials")
    remove.add_argument("--host", default="github.com")
    remove.set_defaults(handler=_command_github_remove)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="diffvouch", description="Local-first AI code reviews")
    parser.add_argument("--version", action="version", version=f"diffvouch {__version__}")
    subparsers = parser.add_subparsers(dest="command", required=True)
    _add_review_parser(subparsers)
    _add_auth_parser(subparsers)
    _add_github_parser(subparsers)
    return parser


def _resolve_local_base(repository: Path, candidate: str) -> str:
    result = subprocess.run(
        ["git", "rev-parse", "--verify", f"{candidate}^{{commit}}"],
        cwd=repository,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    if result.returncode == 0:
        return candidate
    remote_candidate = f"origin/{candidate}"
    result = subprocess.run(
        ["git", "rev-parse", "--verify", f"{remote_candidate}^{{commit}}"],
        cwd=repository,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    if result.returncode == 0:
        return remote_candidate
    raise GitError(
        f"PR base {candidate!r} is not available locally; fetch it explicitly before reviewing"
    )


def _write_output(value: str, path: Path | None) -> None:
    sys.stdout.write(value)
    if path:
        path.expanduser().write_text(value, encoding="utf-8")


def _command_review(args: argparse.Namespace) -> int:
    if args.fail_below is not None and not 1 <= args.fail_below <= 5:
        raise ArgumentError("--fail-below must be between 1 and 5")
    if args.max_diff_bytes is not None and args.max_diff_bytes < 10_000:
        raise ArgumentError("--max-diff-bytes must be at least 10000")
    if args.pr is not None and args.staged_only:
        raise ArgumentError("--pr cannot be combined with --staged-only")
    if args.publish and args.staged_only:
        raise ArgumentError("--publish cannot be combined with --staged-only")

    repository = repository_root()
    base = args.base
    resolved_repo = args.repo
    if args.pr is not None:
        repo_name, pull = resolve_pull_request(
            repository, args.repo, args.pr, explicit_host=args.github_host
        )
        resolved_repo = repo_name
        if pull.get("state") != "open":
            raise GitHubError(f"pull request {args.pr} is not open")
        local_head = run_git(repository, "rev-parse", "HEAD").strip()
        live_head = pull.get("head", {}).get("sha")
        if local_head != live_head:
            raise GitHubError(
                f"local HEAD {local_head} does not match PR head {live_head}; check out the PR head"
            )
        pr_base_sha = pull.get("base", {}).get("sha")
        if not isinstance(pr_base_sha, str):
            raise GitHubError("GitHub did not return the PR base SHA")
        if base:
            local_base = _resolve_local_base(repository, base)
            local_base_sha = run_git(
                repository, "rev-parse", "--verify", f"{local_base}^{{commit}}"
            ).strip()
            if local_base_sha != pr_base_sha:
                raise GitHubError(
                    f"--base resolves to {local_base_sha}, but PR base is {pr_base_sha}"
                )
        try:
            run_git(repository, "cat-file", "-e", f"{pr_base_sha}^{{commit}}")
        except GitError as exc:
            raise GitError(
                f"PR base commit {pr_base_sha} is not available locally; fetch it explicitly"
            ) from exc
        base = pr_base_sha
    if args.publish and not (base or args.pr):
        raise ArgumentError(
            "--publish requires --pr or --base so the reviewed commit is reproducible"
        )

    result = perform_review(
        provider_name=args.provider,
        transport=args.transport,
        model=args.model,
        effort=args.effort,
        base=base,
        committed_only=args.committed_only or args.pr is not None or args.publish,
        staged_only=args.staged_only,
        excludes=args.exclude,
        config_path=args.config,
        max_diff_bytes=args.max_diff_bytes,
        fail_below=args.fail_below,
        fail_on_severity=args.fail_on_severity,
        publication_requested=args.publish,
        root=repository,
    )
    if result is None:
        _write_output("No reviewable changes.\n", args.output)
        return 0

    try:
        if args.publish:
            url, bot = publish_review(
                result,
                repository,
                resolved_repo,
                args.pr,
                explicit_host=args.github_host,
            )
            result.publication.published = True
            result.publication.url = url
            result.publication.bot = bot
    except GitHubError:
        rendered = render_json(result) if args.format == "json" else render_terminal(result)
        _write_output(rendered, args.output)
        raise

    rendered = render_json(result) if args.format == "json" else render_terminal(result)
    _write_output(rendered, args.output)
    return 0 if result.gate.passed else 1


def _command_auth_login(args: argparse.Namespace) -> int:
    if args.provider in {"openai", "codex"}:
        command = ["codex", "login"]
        if args.device:
            command.append("--device-auth")
    else:
        if args.device:
            raise ArgumentError("--device is supported only for OpenAI/Codex login")
        command = ["claude", "auth", "login"]
    try:
        return subprocess.run(command, check=False).returncode
    except FileNotFoundError as exc:
        raise ArgumentError(f"{command[0]} CLI is not installed") from exc


def _auth_status(command: list[str]) -> tuple[bool, str]:
    try:
        result = subprocess.run(command, text=True, capture_output=True, check=False)
    except FileNotFoundError:
        return False, "CLI not installed"
    detail = (result.stdout or result.stderr).strip()
    return result.returncode == 0, detail


def _command_auth_status(args: argparse.Namespace) -> int:
    providers = [args.provider] if args.provider != "all" else ["openai", "claude"]
    config = load_global_config()
    failed = False
    for provider in providers:
        if provider in {"openai", "codex"}:
            ok, detail = _auth_status(["codex", "login", "status"])
            label = "openai (Codex subscription)"
            api_provider = "openai"
        else:
            ok, detail = _auth_status(["claude", "auth", "status", "--text"])
            label = "claude subscription"
            api_provider = "anthropic"
        print(f"{label}: {'ready' if ok else 'not ready'}{f' — {detail}' if detail else ''}")
        api_ready = api_provider in config.get("api_keys", {})
        print(f"{api_provider} API key: {'configured' if api_ready else 'not configured'}")
        failed = failed or not (ok or api_ready)
    return 1 if failed else 0


def _command_set_key(args: argparse.Namespace) -> int:
    if args.stdin:
        secret = sys.stdin.read().strip()
    else:
        secret = getpass.getpass(f"{args.provider} API key: ").strip()
    config = load_global_config()
    previous = config.get("api_keys", {}).get(args.provider)
    reference = store_secret(f"api-key:{args.provider}", secret, args.storage)
    config["api_keys"][args.provider] = reference.as_dict()
    save_global_config(config)
    if isinstance(previous, dict):
        previous_reference = SecretReference.from_dict(previous)
        if previous_reference != reference:
            delete_secret(previous_reference)
    print(f"Stored {args.provider} API key in {reference.backend} storage.")
    return 0


def _command_remove_key(args: argparse.Namespace) -> int:
    config = load_global_config()
    previous = config.get("api_keys", {}).pop(args.provider, None)
    if isinstance(previous, dict):
        delete_secret(SecretReference.from_dict(previous))
        save_global_config(config)
        print(f"Removed {args.provider} API key.")
    else:
        print(f"No stored {args.provider} API key.")
    return 0


def _command_github_create(args: argparse.Namespace) -> int:
    if args.no_browser:
        from diffvouch.github_app import creation_url

        url = creation_url(args.host, args.owner)
    else:
        url = open_creation_page(args.host, args.owner)
    print(f"GitHub App creation page: {url}")
    print("Configure the app as private with:")
    print("  Pull requests: Read and write")
    print("  Contents: No access (or Read-only only if you later enable remote content retrieval)")
    print("  Webhooks: Disabled; subscribe to no events")
    print("Install it on the repositories DiffVouch may review, then run:")
    print("  diffvouch github app configure --app-id ID --slug SLUG --private-key KEY.pem")
    if not args.owner:
        print("For organization repositories, create the private app under that organization.")
    return 0


def _command_github_configure(args: argparse.Namespace) -> int:
    app_id = args.app_id or input("GitHub App ID: ").strip()
    slug = args.slug or input("GitHub App slug: ").strip()
    private_key = args.private_key or Path(input("Downloaded private-key PEM path: ").strip())
    config = configure_app(
        app_id=app_id,
        slug=slug,
        private_key_path=private_key,
        host=args.host,
        storage=args.storage,
        api_base_url=args.api_base_url,
        web_base_url=args.web_base_url,
        api_version=args.api_version,
    )
    print(f"Configured and validated {config.slug}[bot] for {config.host}.")
    print(
        f"Install or update repository access: {config.web_base_url}/apps/{config.slug}/installations/new"
    )
    return 0


def _command_github_status(args: argparse.Namespace) -> int:
    config = load_app(args.host)
    api = GitHubAPI(config)
    app = api.app()
    print(f"App: {app.get('slug', config.slug)}[bot] (ID {app.get('id', config.app_id)})")
    print(f"Host: {config.host}")
    print(f"Private key storage: {config.private_key.backend}")
    if args.repo:
        if "/" not in args.repo:
            raise ArgumentError("--repo must be owner/name")
        owner, name = args.repo.split("/", 1)
        token = api.installation_token(owner, name)
        repository = api.request("GET", f"/repos/{owner}/{name}", token=token)
        print(f"Installation: ready for {repository.get('full_name', args.repo)}")
        print("Installation token: generated in memory and discarded after this command")
    return 0


def _command_github_remove(args: argparse.Namespace) -> int:
    if remove_app(args.host):
        print(f"Removed local GitHub App credentials for {args.host}.")
        print("Revoke the private key in GitHub App settings if it will no longer be used.")
    else:
        print(f"No GitHub App was configured for {args.host}.")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.handler(args))
    except DiffVouchError as exc:
        print(f"diffvouch: {exc}", file=sys.stderr)
        return exc.exit_code
    except KeyboardInterrupt:
        print("diffvouch: interrupted", file=sys.stderr)
        return 130
