#!/usr/bin/env python3
"""A release's notes and its Discord announcement, both from CHANGELOG.md.

    release-announcement.py notes   <version>   the CHANGELOG section, for the GitHub release body
    release-announcement.py discord <version>   the Discord payload as JSON (no network)
    release-announcement.py post    <version>   post it to $DISCORD_ANNOUNCEMENTS_WEBHOOK

<version> has no leading "v" (0.9.3). A stable release must have its CHANGELOG
section; a pre-release without one gets a one-line body and is never announced.
Pass --changelog <path> to read another file (the tests do).

Posting retries a 429 or a 5xx, honouring Discord's retry_after, and never lets
the changelog text mention anyone: allowed_mentions is empty, so an "@everyone"
in a bullet stays text.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

REPO = 'openrmm-project/openrmm'
# Discord allows 4096 characters in an embed description; the changelog part is
# kept well under that so the upgrade lines and the link always fit.
CHANGES_BUDGET = 2500
VERSION = re.compile(r'^\d+\.\d+\.\d+([+-][0-9A-Za-z.-]+)?$')


def is_prerelease(version):
    return '-' in version or '+' in version


def section(version, changelog):
    with open(changelog, encoding='utf-8') as f:
        text = f.read()
    m = re.search(r'^## \[' + re.escape(version) + r'\][^\n]*\n(.*?)(?=^## \[|\Z)', text, re.S | re.M)
    body = m.group(1).strip() if m else ''
    if body:
        return body
    if is_prerelease(version):
        return f'Pre-release {version}. See CHANGELOG.md for the changes so far.'
    raise SystemExit(f'CHANGELOG.md has no section for {version}: add it before tagging')


def for_discord(markdown):
    """CHANGELOG Markdown hard-wraps its bullets; Discord keeps every newline.

    Joins each bullet's continuation lines, turns "### Fixed" into a bold line,
    and leaves links, bold and inline code as they are (Discord renders them)."""
    out, current = [], None

    def flush():
        nonlocal current
        if current is not None:
            out.append(current)
            current = None

    for raw in markdown.splitlines():
        line = raw.rstrip()
        stripped = line.strip()
        if not stripped:
            flush()
            # A blank line separates paragraphs, but not a heading from its items.
            if out and out[-1] != '' and not re.match(r'^\*\*.*\*\*$', out[-1]):
                out.append('')
            continue
        heading = re.match(r'^#{3,6}\s+(.*)$', stripped)
        if heading:
            flush()
            out.append(f'**{heading.group(1).strip()}**')
            continue
        if re.match(r'^[-*]\s+', stripped) and not line.startswith('    '):
            flush()
            current = '- ' + re.sub(r'^[-*]\s+', '', stripped)
            continue
        if current is not None:
            current += ' ' + stripped
        elif out and out[-1] not in ('',) and not out[-1].startswith(('**', '- ')):
            out[-1] += ' ' + stripped
        else:
            out.append(stripped)
    flush()
    while out and out[-1] == '':
        out.pop()
    return '\n'.join(out)


def trim(text, budget, url):
    """Cut at a line boundary, never inside a bullet, and point to the full notes."""
    if len(text) <= budget:
        return text
    kept, size = [], 0
    for line in text.split('\n'):
        if size + len(line) + 1 > budget:
            break
        kept.append(line)
        size += len(line) + 1
    while kept and (kept[-1] == '' or kept[-1].startswith('**')):
        kept.pop()  # no dangling heading or blank line before the cut
    kept.append(f'\n…and more in the [full release notes]({url}).')
    return '\n'.join(kept)


def payload(version, changelog):
    tag = f'v{version}'
    url = f'https://github.com/{REPO}/releases/tag/{tag}'
    changes = trim(for_discord(section(version, changelog)), CHANGES_BUDGET, url)
    upgrade = (
        '**Upgrading**\n'
        '- Self-hosted: take a backup, then `git pull`, `docker compose pull` and '
        '`docker compose up -d` (deploy/QUICKSTART.md). The server migrates the '
        'database when it starts, and the agents update themselves from it.\n'
        '- OpenRMM Cloud: consoles are updated in their nightly maintenance window.'
    )
    return {
        'content': f'**OpenRMM {tag} is out**',
        'embeds': [{
            'title': f'OpenRMM {tag}',
            'url': url,
            'description': f'{changes}\n\n{upgrade}',
            'color': 0x2563EB,
        }],
        # Nothing in the changelog may ping anyone (@everyone, @here, roles, users).
        'allowed_mentions': {'parse': []},
    }


def post(version, changelog):
    if is_prerelease(version):
        print(f'{version} is a pre-release: not announced')
        return 0
    hook = os.environ.get('DISCORD_ANNOUNCEMENTS_WEBHOOK', '').strip()
    if not hook:
        print('::notice::DISCORD_ANNOUNCEMENTS_WEBHOOK is not set: the release is not announced on Discord')
        return 0
    if not hook.startswith('https://discord.com/api/webhooks/'):
        print('::error::DISCORD_ANNOUNCEMENTS_WEBHOOK is not a Discord webhook URL')
        return 1
    body = json.dumps(payload(version, changelog)).encode()
    request = urllib.request.Request(
        hook + '?wait=true', data=body, method='POST',
        headers={
            'Content-Type': 'application/json',
            # Discord's edge refuses some default library user agents.
            'User-Agent': f'DiscordBot (https://github.com/{REPO}, release-announcement)',
        })
    for attempt in range(1, 6):
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                print(f'announced {version} on Discord (HTTP {response.status})')
                return 0
        except urllib.error.HTTPError as err:
            if err.code != 429 and err.code < 500:
                # The body may describe the request; it never contains the URL.
                print(f'::error::Discord refused the announcement: HTTP {err.code}')
                return 1
            wait = 2 ** attempt
            try:
                wait = float(json.loads(err.read() or b'{}').get('retry_after', wait))
            except (ValueError, AttributeError):
                pass
            print(f'Discord answered HTTP {err.code}; retrying in {wait:.1f}s ({attempt}/5)')
            time.sleep(min(max(wait, 1.0), 60.0))
        except urllib.error.URLError as err:
            print(f'Discord unreachable ({type(err.reason).__name__}); retrying ({attempt}/5)')
            time.sleep(2 ** attempt)
    print('::error::could not announce the release on Discord after 5 attempts')
    return 1


def main(argv):
    changelog = 'CHANGELOG.md'
    if '--changelog' in argv:
        i = argv.index('--changelog')
        changelog = argv[i + 1]
        argv = argv[:i] + argv[i + 2:]
    if len(argv) != 2 or argv[0] not in ('notes', 'discord', 'post') or not VERSION.match(argv[1]):
        print(__doc__, file=sys.stderr)
        return 2
    mode, version = argv
    if mode == 'notes':
        print(section(version, changelog))
        return 0
    if mode == 'discord':
        print(json.dumps(payload(version, changelog), indent=2, ensure_ascii=False))
        return 0
    return post(version, changelog)


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
