# Development helpers

## `parity_oracle.py`

Generates the golden file used by `TestParityAgainstPythonServer` in
`internal/tools`.

The Python MCP server is being replaced by the Go implementation, so its
behaviour is frozen as a test oracle before it is deleted. The script imports
the **real** `whatsapp-mcp-server/whatsapp.py` (it needs only `requests`, which
is usually already present) and drives it against a fixture database.

Regenerate the golden file whenever the Python server changes:

```bash
python3 dev/parity_oracle.py <path-to-messages.db> \
  > internal/tools/testdata/python_oracle.json
```

Build the fixture database that the Go test uses with the same data as
`writeParityFixture` in `internal/tools/parity_test.go`. Any mismatch between
the two sides fails the test and points at the exact key that drifted.

After the Python server is removed (Phase 6), the golden file remains as the
recorded contract; the regeneration step is then historical only.
