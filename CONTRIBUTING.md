# Contributing

This repository is a published copy of the VAIF Studio Go SDK. VAIF Studio LLC
maintains the SDK in its main source repository and publishes each release
here.

## Bug reports and feature requests

Bug reports and feature requests are welcome as
[GitHub issues](https://github.com/vaifstudio/vaif-go/issues). For a bug,
include:

- the SDK version and your Go version and operating system;
- a minimal program or test that reproduces the problem;
- what you expected and what happened, including the error's `Code` and
  `RequestID` when there is one.

Remove access tokens, refresh tokens, API keys and other credentials from
anything you post. Report security vulnerabilities privately as described in
[SECURITY.md](SECURITY.md), not in an issue.

## Pull requests

Pull requests are welcome. For a change larger than a small fix, open an issue
first so the approach can be agreed before you write the code.

The maintainers review pull requests in this repository. Each release
replaces the contents of this repository with an export from the main source
repository, committed on top of the existing history, so an accepted pull
request is not merged here: the maintainers apply the change to the main
source repository, credit you as a co-author of that commit, and close the
pull request when the change is published.

A pull request should:

- keep to one change, with tests that fail without it;
- pass the checks below;
- not edit `operations_generated.go` by hand. That file is generated from the
  VAIF Studio API's OpenAPI description; describe the change you need, and the
  maintainers regenerate it.

## Local development

You need Go 1.26.10 or later.

```sh
git clone https://github.com/vaifstudio/vaif-go.git
cd vaif-go
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -scan module
```

Two tests read files that exist only in the main source repository, the
OpenAPI description and the shared conformance cases. They run when
`VAIF_OPENAPI_SPEC` and `VAIF_CONFORMANCE_CASES` name those files, and are
skipped otherwise.

The integration tests in `integration_test.go` run against a real VAIF Studio
API when `VAIF_IT_BASE_URL`, `VAIF_IT_PROJECT_ID` and `VAIF_IT_API_KEY` are
set. The key needs the crud, storage and functions scopes, and the project
needs a table with a text column `name`, named by `VAIF_IT_TABLE` (default
`sdk_items`). The tests create users, table rows and storage objects in that
project, so use a project that holds no data you need.

`cmd/conformance` is the adapter that the maintainers' cross-SDK conformance
runner drives against a local API. It is not part of the SDK's API, and it
accepts only a loopback `API_URL`.

## Code of conduct

This project follows the [code of conduct](CODE_OF_CONDUCT.md).

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE) that covers this repository.
