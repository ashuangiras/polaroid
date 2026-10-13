# Git remote smoke test

This procedure checks knowledge synchronisation ([ADR-0046](../architecture/decisions/0046-git-knowledge-synchronisation.md)) against a real hosted Git remote, such as a GitHub repository, over HTTPS. It uses ordinary Git over HTTPS and no GitHub API. The automated tests already cover local, HTTPS (`git http-backend`) and SSH remotes.

Run it only with a repository and token that the owner supplies for this purpose.

- **Do not use the Polaroid source repository.** It is public and must never receive knowledge.
- **Do not create a repository or change permissions** to run this test. The owner creates both the repository and the token.
- **Use a private, empty test repository,** or a dedicated branch in one.

## What it does

`TestGitHubSmoke` in [internal/knowledgesync/measure_test.go](../../internal/knowledgesync/measure_test.go) runs two independent instances, A and B, each with its own temporary Git-core store.

1. A publishes two procedures as `polaroid.smoke`.
2. A pushes them to the branch.
3. B fetches, validates and activates them, and checks that it reads exactly A's knowledge.
4. A publishes an improvement and pushes it.
5. B transfers and activates the improvement incrementally.
6. A fetch with a wrong token must fail as unauthorised.

The test only adds commits to the named branch, and only as fast-forwards. It never forces, deletes or rewrites anything. Each round logs the object count, the bytes received and the round's duration.

## Run it

1. Create a private repository, or choose a new branch name in one. A branch that already holds other history makes the first push fail with "the remote holds snapshots not fetched here"; this is intended.
2. Create a token that can read and write that repository's contents, and nothing else. For GitHub, use a fine-grained personal access token limited to that repository, with **Contents: read and write**.
3. Run the test from the repository root, with the token in the environment and not on the command line:

   ```sh
   read -s "POLAROID_SMOKE_TOKEN?Token: " && export POLAROID_SMOKE_TOKEN
   POLAROID_SMOKE_URL=https://github.com/OWNER/REPO.git \
   POLAROID_SMOKE_BRANCH=polaroid-smoke-$(date +%Y%m%d%H%M%S) \
   go test -count=1 -v -run TestGitHubSmoke ./internal/knowledgesync/
   unset POLAROID_SMOKE_TOKEN
   ```

   `read -s "VAR?prompt"` is the zsh form; in bash, use `read -s -p "Token: " POLAROID_SMOKE_TOKEN`. `POLAROID_SMOKE_USER` is optional; it defaults to `git`, which GitHub accepts with a token.
4. The test passes when both rounds read the same knowledge on A and B and the wrong token is refused. Record the logged sizes and durations with the run.
5. When you are done, delete the test branch, or revoke the token, through the host's own interface.
