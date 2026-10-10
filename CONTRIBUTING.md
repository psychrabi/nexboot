# Contributing to NexBoot

Bug reports, fixes, documentation improvements, and feature proposals are welcome.

## Before opening an issue

- Search existing issues first.
- For boot problems, include the client firmware (BIOS or UEFI), NIC model, operating system image and build, and the error shown on the client screen.
- Attach server logs from `journalctl -u ndiskless --since "-30min"`.

## Pull requests

Use the Go version specified in `go.mod`, along with Node.js 22 (at least 22.22.2) or 24 (at least 24.15.0) and npm. The Makefile automatically builds and embeds the frontend; do not commit `web/dist` or other generated files.

1. Open an issue before making a substantial change so the approach can be agreed upon.
2. Keep each pull request focused on one change.
3. Write a failing test before implementing the fix.
4. Run the same checks as CI before pushing:

   ```sh
   make verify-all   # gofmt, vet, Go tests, static build, frontend tests/build
   ```

5. Write concise code comments **in English**, explaining why rather than what the code does.

Features that require real ZFS, LIO, or multiple machines are covered by end-to-end test matrices in [`tools/e2e/`](tools/e2e/README.md). State in the pull request whether these tests were executed.

By contributing, you agree that your work is licensed under [AGPL-3.0](LICENSE).
