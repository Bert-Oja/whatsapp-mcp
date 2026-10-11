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

This endpoint requires a complete body, and `restrictions` must be either an
object or literal JSON `null`. `gh`'s `-F` cannot express `null`: an empty value
becomes the empty string, which the API rejects with

```
No subschema in "anyOf" matched.
For 'allOf/0', "" is not an object.
For 'anyOf/1', "" is not a null.
```

Write the body to a file and pass it with `--input`:

```bash
cat > protection.json <<'JSON'
{
  "required_status_checks": { "strict": true, "contexts": ["test"] },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false,
    "required_approving_review_count": 0
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true
}
JSON

gh api -X PUT repos/Bert-Oja/whatsapp-mcp/branches/main/protection \
  --input protection.json
```

Notes on the choices:

- `strict=true` requires the branch to be up to date with `main` before merging,
  so a green check on a stale branch does not count.
- `required_approving_review_count=0` permits self-merge on a solo repo while
  still requiring a PR, which is the only way to get squash-only + linear
  history. Raise it to 1 if anyone else ever contributes.
- `required_linear_history=true` blocks pushing merge commits directly, so
  history on `main` stays linear even outside the squash path.
- `enforce_admins=true` means the rules apply to the owner too. Drop it only if
  you want an escape hatch.
- `restrictions: null` keeps pushes open to collaborators; the PR requirement is
  what actually gates `main`.

Only `-F` fields can be combined on the command line; `restrictions` cannot, so
use `--input` for the whole body rather than mixing the two.

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
