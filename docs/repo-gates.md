# Repo gates to apply

The `gh` credentials available to this session are a GitHub App installation
token (`miyadaiku-dev[bot]`, App ID 5133940) with `repo` scope but no
administration permission. Every write to repo settings and branch protection
returns `403 Resource not accessible by integration`, via both the classic
branch-protection API and the newer rulesets API.

The commands below must be run by an account with admin rights on
`Bert-Oja/whatsapp-mcp`.

## 1. Squash-only merges

```bash
gh api -X PATCH repos/Bert-Oja/whatsapp-mcp \
  -f allow_squash_merge=true \
  -f allow_merge_commit=false \
  -f allow_rebase_merge=false \
  -f delete_branch_on_merge=true \
  -f squash_merge_commit_title=PR_TITLE \
  -f squash_merge_commit_message=PR_BODY
```

`allow_merge_commit=false` and `allow_rebase_merge=false` leave squash as the
only option, which is what a linear history on `main` needs.

## 2. Protect main

The CI check is named **`test`** (the job name in `.github/workflows/ci.yml`).

```bash
gh api -X PUT repos/Bert-Oja/whatsapp-mcp/branches/main/protection \
  -H "Accept: application/vnd.github+json" \
  -F 'required_status_checks[strict]=true' \
  -F 'required_status_checks[contexts][]=test' \
  -F 'enforce_admins=true' \
  -F 'required_pull_request_reviews[required_approving_review_count]=0' \
  -F 'required_pull_request_reviews[dismiss_stale_reviews]=true' \
  -F 'restrictions=' \
  -F 'allow_force_pushes=false' \
  -F 'allow_deletions=false' \
  -F 'required_conversation_resolution=true'
```

Notes on the choices:

- `strict=true` requires the branch to be up to date with `main` before merging,
  so a green check on a stale branch does not count.
- `required_approving_review_count=0` permits self-merge on a solo repo while
  still requiring a PR, which is the only way to get squash-only + linear
  history. Raise it to 1 if anyone else ever contributes.
- `enforce_admins=true` means the rules apply to the owner too. Drop it only if
  you want an escape hatch.
- Leaving `restrictions` empty keeps pushes open to collaborators; the PR
  requirement is what actually gates `main`.

## 3. Verify

```bash
gh api repos/Bert-Oja/whatsapp-mcp --jq \
  '{squash:.allow_squash_merge, merge:.allow_merge_commit, rebase:.allow_rebase_merge}'
# expect: {"squash":true,"merge":false,"rebase":false}

gh api repos/Bert-Oja/whatsapp-mcp/branches/main/protection --jq \
  '{checks:.required_status_checks.contexts, strict:.required_status_checks.strict, admins:.enforce_admins.enabled}'
# expect: {"checks":["test"],"strict":true,"admins":true}
```

## Caveat: this repo is a fork

`Bert-Oja/whatsapp-mcp` is a fork of `lharries/whatsapp-mcp`. Some settings are
restricted on forks, and some Actions features may require enabling in the
repository settings. Actions itself is already enabled here and CI has run green.
