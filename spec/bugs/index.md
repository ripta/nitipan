# Bugs

Each bug is a file named `YYYYMMDD-short-name.md`, where the date is when the bug was first observed. The file's
`**Status:**` line is the authority. The table below mirrors it. A status is `open`, `accepted`, `fixed`, or
`not-reproducible`.

| Bug | Summary | Status |
|-----|---------|--------|
| [20261114-who-am-i-ignores-json](20261114-who-am-i-ignores-json.md) | `who am-i --json` accepts the flag and prints text | open |
| [20261114-acting-for-help-says-scope](20261114-acting-for-help-says-scope.md) | `acquire --acting-for` help says "scope" where nitipan means reach | open |
| [20261114-revoke-user-owner](20261114-revoke-user-owner.md) | `broker revoke-user` accepts the broker owner's own user ID | open |
| [20261114-cross-user-help-order](20261114-cross-user-help-order.md) | `broker cross-user` help says it lists unbound labels first, but it commits first | open |
| [20261114-cross-user-flag-never-clears](20261114-cross-user-flag-never-clears.md) | The plain-HTTP refusal offers `node rekey` as a way out, but nothing clears the flag | open |
| [20261114-nonff-branch-refused-as-extra-fence](20261114-nonff-branch-refused-as-extra-fence.md) | A behind branch makes the pre-push hook refuse the fence ref as one the leases do not call for | open |
