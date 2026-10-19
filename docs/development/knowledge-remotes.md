# Knowledge remotes

This is how an operator shares Git-native knowledge ([ADR-0045](../architecture/decisions/0045-living-knowledge-kernel.md)) between Polaroid instances through an ordinary Git remote ([ADR-0046](../architecture/decisions/0046-git-knowledge-synchronisation.md)), such as a private GitHub repository. The remote only transports knowledge. Each instance stays the authority for its own active knowledge. For setting up and using Git-native knowledge in the first place (connecting, discovering, reading, resolving, publishing), see [knowledge-guide.md](../architecture/knowledge-guide.md); this page covers only exchanging it between instances.

- **Agents are unaffected.** They keep using `discover`, `read` and `feedback` at `/mcp/agent`, and they read only the instance's active, local knowledge. They never need remote credentials.
- **No connectivity needed for reads.** When the remote is unreachable, only the operator steps below fail.
- **Nothing runs on its own.** Every exchange is an explicit operator step at `/v1/knowledge/*` (`polaroid knowledge-*`), authenticated with the instance's operator token (`POLAROID_KNOWLEDGE_TOKEN`). There is no background synchronisation.
- **Never use the Polaroid source repository as a remote.** It is public.

## Configure a remote

The daemon reads its trust policy at start from `<knowledge-dir>-sources.json` (by default `~/.polaroid/data/knowledge-sources.json`). A missing file means no sources. The file is a JSON array; unknown members are refused.

```json
[
  {"name": "corpus", "url": "https://github.com/OWNER/REPO.git", "branch": "main",
   "publishers": ["operator.alice"], "publish": true}
]
```

| Member | Meaning |
| --- | --- |
| `name` | The source's name in the `knowledge-*` commands; unique. |
| `url` | HTTPS URL, SSH URL or absolute local path. A URL that holds credentials is refused. |
| `branch` | The remote branch, `main` if omitted. The instance's own active knowledge is `refs/heads/active`. What the remote is known to hold is tracked at `refs/remotes/<name>/<branch>`. |
| `publishers` | The publication authors accepted from this source: the `author` of each publication, as recorded in its commit. Validation rejects any incoming publication by another author. |
| `publish` | `true` lets this instance push to the source; otherwise a push is refused (`403 forbidden`). |

The file holds no credentials. Create it with mode `0600`, then restart the service (`polaroid restart`) to load it. `polaroid knowledge-status` lists the configured sources.

**Trust boundary:** the trust check is the author name in the commit, not a signature. Anyone who can write to the remote can claim an allowed name. Trust therefore rests on write access to the remote and on the `publishers` list together; keep write access to the remote limited to operators.

## Credentials

`knowledge-fetch` and `knowledge-push` take credentials for that call only: `{"credentials": {"username", "password", "ssh_key", "ssh_passphrase", "known_hosts"}}`. The daemon never stores, logs or returns them.

- **SSH:** needs the private key and a `known_hosts` that verifies the server.
- **HTTPS:** pass an access token as `password`.

Pass the JSON on standard input with `-`, so the secret never appears in a command line, a file or the output. For example, with the GitHub CLI's stored token for an account that can access the repository:

```sh
gh auth token -u ACCOUNT | jq -Rc '{credentials: {username: "x-access-token", password: .}}' | polaroid knowledge-push corpus -
```

A fetch from a public or local remote needs no credentials: `polaroid knowledge-fetch NAME`.

## Author and publish

1. **Draft.** Write the change as a publication request, as described in [http-api.md](../architecture/http-api.md#git-native-knowledge-operator-api):
   - `base` is the active snapshot from `knowledge-status`.
   - `author` is one of the remote's `publishers`.
   - `summary` and `rationale` are both required; validation on other instances rejects a publication without a rationale.
   - Add `finding` when the change addresses a feedback report.
2. **Approve.** The owner approves the change before an operator publishes it. The sole narrow exception is `polaroid.session-close-capture` for `github.com/ashuangiras/polaroid`, which has standing authorization to publish locally after its complete field review and validation pass. That does not authorize a remote push; every sync remains a separate explicit operator action.
3. **Publish locally:** `polaroid knowledge-publish FILE`. The kernel validates the complete catalog before the active ref moves.
4. **Review what will leave the machine:**

   ```sh
   git --git-dir ~/.polaroid/data/knowledge log -p refs/remotes/NAME/BRANCH..refs/heads/active
   ```

   Before the first push, review the whole history, `refs/heads/active`. It must hold no credentials, private paths or personal data. Published history is never rewritten.
5. **Push:** `... | polaroid knowledge-push NAME -`. A push is only ever a fast-forward of the remote branch; it is never forced. To an empty repository, the first push sends the whole history.

## Receive on another instance

1. **Fetch:** `... | polaroid knowledge-fetch NAME -`. The knowledge is transferred into quarantine, within size limits. Nothing local changes.
2. **Validate:** `polaroid knowledge-validate NAME`. This checks Git integrity, that every incoming publication's author is in `publishers` and has a rationale, and that every incoming snapshot is a complete, valid catalog. `status` is then one of:
   - `fast-forward`: can be activated.
   - `identical` or `local-ahead`: nothing to do.
   - `diverged`: stop; see below.
   - `rejected`: the reasons are in `problems`, and nothing was stored.
3. **Activate:** `polaroid knowledge-activate NAME FILE`, where FILE holds the decision `{"author", "summary", "rationale"}`. The active ref moves to the remote snapshot. The decision and the knowledge's origin are journaled; see `polaroid knowledge-activations`.
4. **Check:** agents now `discover` and `read` the new snapshot. Earlier snapshots stay readable.

## When something fails

| Result | Meaning and action |
| --- | --- |
| `401 unauthorized` | A missing or wrong operator token, or credentials the remote refused. Nothing changed. Check the token, the account's access to the repository and the credential's expiry. |
| `403 forbidden` | The policy does not allow pushing to that source. |
| `409 behind` on push | The remote holds publications this instance has not activated. Fetch, validate and activate them first, then push again. |
| `409 diverged` | Both sides published independently. Polaroid never merges or overwrites. Decide which history to keep, then republish the other side's changes on top of it as new publications. Never force-push. |
| `409 stale_snapshot` | The active knowledge changed while the step ran. Repeat the step. |
| Remote unreachable | Nothing local changes, and agents keep reading the active knowledge. Retry the operator step later. |

## Backups

`polaroid backup` copies only the SQLite catalog. Back up the knowledge directory separately, for example with `cp -Rp ~/.polaroid/data/knowledge BACKUP/`. A remote also holds every snapshot that was pushed to it, but nothing published after the last push.
