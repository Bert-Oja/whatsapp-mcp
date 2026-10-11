# Development helpers

## The parity oracle

`internal/tools/testdata/python_oracle.json` is the recorded behaviour of the
original Python MCP server, captured before it was removed.

The Go implementation replaced that server, so the captured output is kept as a
frozen contract and checked by `TestParityAgainstPythonServer` in
`internal/tools`. The test rebuilds the same fixture database the oracle was
generated from and compares each tool's output; a mismatch fails and points at
the exact key that drifted.

The generator script (`dev/parity_oracle.py`) is gone: it imported the Python
server, which no longer exists, so the golden file can no longer be regenerated.
If a tool's contract changes deliberately, update the golden file by hand and
say why in the commit message.
