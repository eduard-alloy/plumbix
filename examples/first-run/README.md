# First run

From nothing to an approved verdict and a merge, on your machine. Needs Docker running, git, curl and a `plumbix` binary: the one you [downloaded](../../README.md#quickstart), or `make build` in this checkout, which puts one in `bin/`. With the images present the run takes about ten seconds; a cold machine pays once for the `golang` image pull and the reference validator build, which is mostly your connection's speed.

```sh
examples/first-run/first-run.sh
```

What it does, in order: starts a Plumbix server of its own on port 6410 with its data under `$TMPDIR/plumbix-first-run`, so nothing you already run is touched; builds the reference validator image from the sources embedded in the server if it is missing; creates a bare git repository with the sample ledger and its pipeline; posts the acceptance criteria as a criteria set; pushes the change on a branch; submits it naming the set; waits for the verdict; merges; and leaves the server running with a sign-in link so you can look at the change in the browser. `--stop` stops the server at the end; a run that fails stops it too. Running the script again stops the server a previous run left and starts afresh. `PLUMBIX_EXAMPLE_BUILD_IMAGE=yes` builds the validator image through the server even when it is present, which is how the test covers that path.

## The files

- `repo/`: the sample repository as it is on `main`. One function, `Balance`, with no test, and `.plumbix/pipeline.yaml`, the smallest useful pipeline: a `test` step in a `golang` container and the reference validator.
- `_change/ledger_test.go`: the change, a test for `Balance`. It goes on the branch `balance-test`. The directory starts with an underscore so Go tools ignore it at the root of this module.
- `criteria.json`: the acceptance criteria as a criteria set, posted before the change exists. Both criteria name the `test` step, so the reference validator proves them from the step's outcome.
- `change.json`: the submission: branches, title, intent, and the criteria set by id. The script fills in the repository and set ids.
- `first_run_test.go`: an integration test that builds the binary and runs the script, so the example cannot rot. `make integration` runs it.

The same steps by hand are in the [README's quickstart](../../README.md#quickstart).
