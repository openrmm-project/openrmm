# 18 — Signed Update Runbook (production)

Operational day-2 procedures for the signed auto-update path: custody the signing
key, cut a signed release, publish artifacts to the production `/dl/`, and rotate
the key. This complements [13-security-rollout](13-security-rollout.md), which
describes the controls; this document is the step-by-step operator playbook.

The trust model: agents pin a **set** of Ed25519 **update public keys** at
install time. Every self-update downloads `<server>/dl/rmm-agent-<os>-<arch>`
**and** its `.sig`, verifies the detached signature against the pinned keys,
validates the version is a forward upgrade, and only then swaps the binary. An
update is accepted if it verifies under **any** pinned key. Trusting a set (not a
single key) is what lets you rotate the signing key **without reinstalling the
fleet**: publish the new key alongside the old, cut a release signed by the new
key, and drop the old key once the fleet has converged (see
[Key rotation](#key-rotation)).

## Phase 0 — Generate and custody the key (once)

1. Build the signer: `make signer` → `bin/rmm-sign`.
2. Generate the keypair:

   ```bash
   ./bin/rmm-sign -gen-key
   ```

   Output:

   ```text
   RMM_UPDATE_PUBLIC_KEY=<base64>   # public — safe to embed and distribute
   RMM_SIGNING_KEY=<base64>         # PRIVATE — crown jewel; root over the fleet
   ```

3. Custody the private key strictly. Never commit it, never place it in the
   server `.env`, never leave it on a build host. `deploy/secrets/` and `*.key`
   are `.gitignore`d if you must stage it locally.

   **Recommended (turnkey, OSS-friendly): sign in GitHub Actions.** Store the
   private key as a secret in a **protected `release` Environment** (below). This
   is automated, reproducible and auditable, and needs no separate machine — the
   right default for a self-hosted project. A GitHub secret is not more *isolated*
   than an offline key (anyone who can alter the workflow or compromise the runner
   could read it), so the protection comes from the controls around it: the
   protected Environment with required reviewers, SHA-pinned actions, and branch
   protection so only reviewed `main` commits reach a release.

   **Optional hardening (suggestions, not required):**
   - An **offline/air-gapped signing machine**: the raw key never enters CI. See
     [Optional: offline signing](#optional-offline-signing).
   - A **hardware token** (YubiKey/YubiHSM, which support Ed25519): the key is
     generated on-device and is non-extractable.
   - **OIDC → cloud KMS** signing keeps it fully automated in GitHub with a
     non-extractable key, but the cloud KMS providers sign with ECDSA/RSA rather
     than Ed25519, so it would require migrating the signature scheme.

   `RMM_UPDATE_PUBLIC_KEY` is safe to share. Losing the private key means you can
   no longer publish updates; leaking it means an attacker can push agent code to
   the whole fleet. If it is ever exposed, rotate it — the agent trusts a key set,
   so rotation needs no fleet reinstall (see [Key rotation](#key-rotation)).

## Phase 1 — Release pipeline (CI signing, recommended)

4. Store secrets in GitHub → Settings → Secrets and variables → Actions:
   - `RMM_SIGNING_KEY` = private key
   - `RMM_UPDATE_PUBLIC_KEY` = public key

   The build embeds the public key; the `publish` job signs and asserts that the
   private key derives the configured public key before publishing.
5. **Protection:** the `publish` job in `.github/workflows/release.yml` declares
   `environment: release`. Create that Environment (Settings → Environments →
   `release`) with **required reviewers** and, ideally, a deployment branch/tag
   rule limiting it to `v*` tags, then move `RMM_SIGNING_KEY` into the
   Environment's secrets. Environment secrets override repo secrets, so signing
   then requires an approval step. `RMM_UPDATE_PUBLIC_KEY` may stay a repo secret
   (it is non-sensitive and is embedded in binaries anyway).
6. Releases only fire from a semantic `v*` tag whose commit is on `main`; the
   workflow re-checks ancestry and runs race/vet/test plus migration, installer
   and deployment gates before building.

## Phase 2 — Cut a signed release

7. Keep `agentVersion` in `cmd/agent/main.go` consistent with the tag (currently
   `0.4.5`).
8. Tag and push:

   ```bash
   git tag v0.4.5 && git push origin v0.4.5
   ```

   The workflow runs gates → builds (public key embedded) → generates the SBOM
   (Syft) and `SHA256SUMS` → signs `rmm-agent-*`, `rmm-remote-*` and
   `SHA256SUMS` → attaches build-provenance attestations → publishes a GitHub
   Release. The signing step is gated behind the `release` Environment approval.

   - **Local alternative** (weaker key hygiene — key touches the build host):

     ```bash
     VERSION=v0.4.5 RMM_SIGNING_KEY=... RMM_UPDATE_PUBLIC_KEY=... make dist-signed
     ```

     Produces the same set in `deploy/public/`: binaries + `.sig`, `SHA256SUMS`
     + `SHA256SUMS.sig`. It does not generate the SBOM or provenance.

## Phase 3 — Publish artifacts to the production `/dl/`

The production edge serves `/dl/` from `/usr/share/nginx/dl`, bind-mounted from
`./public` next to `deploy/docker-compose.prod.yml`. Place the following there
(binary **plus its `.sig`** for every agent/sidecar):

- `rmm-agent-<os>-<arch>` + `.sig`
- `rmm-remote-<os>-<arch>` + `.sig`
- `SHA256SUMS` + `SHA256SUMS.sig`
- `install.sh`, `install.ps1`
- `SBOM.spdx.json`

Procedure:

1. Download the artifacts from the GitHub Release.
2. **Verify before publishing.** Confirm `sha256sum -c SHA256SUMS` passes and the
   `SHA256SUMS.sig` verifies against your public key. Do not publish an artifact
   whose signature you have not checked.
3. Publish the **ticket-aware sidecar (`rmm-remote`) with or before** the agent —
   the native-session handshake depends on it.
4. Copy files into the server's `public/` directory (the bind-mounted `/dl/`).
   Both the binary and its `.sig` must be present; a self-update fetches the
   `.sig` alongside the binary and refuses the update if it is missing.

`/dl/` must be HTTPS. The signature protects the binary's integrity, not the
transport; TLS is still required and installers reject a non-HTTPS origin.

## Phase 4 — Server in production mode

- `RMM_PUBLIC_URL` on `https://`, TLS terminated at the trusted edge.
- Once the whole fleet reports the ticket-aware build, set
  `RMM_ALLOW_LEGACY_AGENT_RELAY=false` (default). Keep it `true` only during the
  controlled sidecar rollout.

## Phase 5 — Fleet bootstrap (the one manual step)

Reinstall each endpoint once with the signed installer and the pinned public key.
This installs the signed build in `strict` mode, records the `.sig` proof and
pins the trusted key. Already-enrolled hosts keep their identity (`state.json` is
reused); the installer still requires `--token` (any non-empty string suffices in
that case). New hosts use a console enrollment token.

Linux/macOS:

```bash
curl -fsSL https://agents.rmm.example.com/install.sh | sudo bash -s -- \
  --server https://agents.rmm.example.com \
  --token '<ENROLL_TOKEN>' \
  --update-public-key '<RMM_UPDATE_PUBLIC_KEY>' \
  --require-tls
```

Windows (elevated PowerShell):

```powershell
& ([scriptblock]::Create((iwr -UseBasicParsing https://agents.rmm.example.com/install.ps1))) `
  -Server https://agents.rmm.example.com `
  -Token '<ENROLL_TOKEN>' `
  -UpdatePublicKey '<RMM_UPDATE_PUBLIC_KEY>' `
  -RequireTls
```

`--update-public-key`/`-UpdatePublicKey` accepts several comma-separated keys and
may be repeated; the installer verifies the download under any of them and pins
the whole set. This is only needed during a [key rotation](#key-rotation) — a
first install pins one key.

## Phase 6 — Validate auto-update end to end

1. Confirm the console shows devices reporting the new version.
2. Exercise the cycle: cut a trivial `v0.4.6`, publish the signed artifacts to
   `/dl/`, and confirm agents update within `RMM_UPDATE_INTERVAL` (default 30
   min; first check ~60s after connect). Agent logs should show
   `security: verified update signature for …/dl/rmm-agent-…` followed by
   `update: new agent staged; restarting`. Auto-update honors `RMM_AUTO_UPDATE=0`
   to disable and `RMM_UPDATE_INTERVAL` to tune cadence.
3. For a large fleet, roll out in phases — a canary cohort before 100%.

## Key rotation

Agents trust a **set** of pinned keys and accept an update signed by any of them,
so rotation is a staged, online operation — **no fleet reinstall required**:

1. **Generate** a new keypair (Phase 0) and custody the new private key. Keep the
   old key active for now.
2. **Widen trust to both keys.** Push a service-config change so every agent runs
   with both public keys pinned. The installer, the `-update-public-key` flag and
   `RMM_UPDATE_PUBLIC_KEY` all accept several keys separated by commas (or
   whitespace/semicolons):

   ```text
   -update-public-key '<OLD_PUBLIC_KEY>,<NEW_PUBLIC_KEY>'
   ```

   Deliver this to the fleet the same way you manage the rest of the agent
   service configuration (config management, or a re-run of the installer with
   both keys — identity is preserved). At this stage releases are still signed by
   the **old** key, so nothing breaks while the new key propagates.
3. **Switch signing to the new key.** Once the whole fleet reports both keys
   pinned, cut and publish a release signed by the **new** key (Phases 2–3).
   Agents verify it under the new key in the ring. Confirm logs show
   `security: verified update signature … (key <fingerprint>)` with the new key's
   fingerprint (first 4 bytes of `sha256(pubkey)`, hex).
4. **Narrow trust to the new key.** Push a config change dropping the old key so
   agents pin only `<NEW_PUBLIC_KEY>`.
5. **Retire** the old private key.

Because rotation no longer needs a maintenance window, rotate on a schedule and
immediately on suspected compromise. Always overlap: never remove a key from the
ring before the fleet is signing with, and verifying under, its replacement.

## Optional: offline signing

A stronger key custody for operators who want it — the raw key never enters CI.
It trades the turnkey CI flow for a manual step, so it is a suggestion, not the
default.

1. Let GitHub Actions build the **unsigned** artifacts and provenance (remove or
   skip the signing step in the `publish` job so no private key lives in GitHub).
2. On the offline/air-gapped machine, download the release artifacts and verify
   the checksums.
3. Sign there with the tooling that already exists:

   ```bash
   VERSION=v0.4.5 RMM_SIGNING_KEY=... RMM_UPDATE_PUBLIC_KEY=... make dist-signed
   # or, over already-built artifacts staged in deploy/public/: make sign-dist
   ```

4. Publish the binaries **plus their `.sig`**, `SHA256SUMS` and `SHA256SUMS.sig`
   to `/dl/` (Phase 3).

Keep that machine's disk encrypted, off the network, and access-restricted; a
neglected offline box is often *less* safe than a well-controlled GitHub secret.
A hardware token (YubiKey/YubiHSM, both support Ed25519) is the non-extractable
version of the same idea.

## Verification modes and auto-update

| Mode | Signature | Rollback transaction |
|---|---|---|
| `strict` (default) | required; a bad or missing one aborts the update | signed both ways, re-verified before any rollback |
| `warn` | verified, failure logged rather than fatal | signed both ways |
| `off` | not fetched, not checked | **hashes only** |

`off` is lab-only, and what `install.ps1 -LabMode` selects when no update key is
pinned. Auto-update works there, but the rollback record carries integrity
(hashes) without authenticity (signatures) — because the agent in that mode
already installs whatever `/dl/` serves without checking who signed it, there is
no authenticity to record.

A transaction *recorded* with signatures is always validated with them, whatever
the agent's current mode. Flipping an agent to `off` therefore cannot turn a
signed record into an unchecked one; the check keys on what was written, not on
how the agent happens to be running now.

## Guardrails

- Never set `RMM_UPDATE_VERIFY=off` in production; keep it `strict`.
- Never serve `/dl/` over plain HTTP.
- Never publish an agent without its `.sig`, or a sidecar older than the agent
  that depends on it.
- Keep the private key out of the repo, the server `.env` and build hosts.
