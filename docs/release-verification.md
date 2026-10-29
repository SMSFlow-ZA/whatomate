# Release verification

The release workflow invokes the reusable Test workflow at the release tag before
any publishing job runs. Its PostgreSQL/Redis race suite, lint and build therefore
check the same source revision as GoReleaser. Required-check status on a branch
is not substituted for a fresh tagged-source run. A failed check prevents entry
to the release job and its registry login/publishing steps.

Existing image names, release formats, tag triggers and registry credentials
remain unchanged. This adds no deployment target or hosting-provider workflow.
Local review can validate workflow structure and run the existing Go checks;
actual GitHub job behavior is verified only by an authorized future workflow run.
