# interior_api — instructions for Claude Code

**Read `docs/PROJECT_STATUS.md` first, completely.** It is the project's working memory:
what is built (backend Phases 0–10, all merged), every agreed decision, how we work, setup on a
new device (now a Mac), and the contract the Next.js frontend in `../interior_web` depends on.
Then confirm against `git log` and the code.

Rules that must never be missed:

- **No `Co-Authored-By:` lines or any Claude/"Generated with Claude Code" attribution** in
  commits or PRs. Commits are authored by the developer only.
- **Never push.** The developer pushes.
- **Backend only.** No frontend files and no Docker/WSL unless the developer explicitly asks.
- **One phase at a time:** decisions → plan → approval → build on branch `phase-N` → verify →
  report → merge (`--ff-only`) only when the developer says "merge".
- Within an approved phase, work freely on local changes and report afterwards; ask before
  anything outward-facing or installing software.
- The developer is learning Go: explain non-obvious Go choices as you implement them.
- Verify before claiming: `gofmt -l .` (empty), `go vet ./...`,
  `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`, `go test ./...` with
  `TEST_DATABASE_URL` set. Say plainly what was not run.
