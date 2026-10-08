package cli

// Aliases exposing internal identifiers to the external cli_test
// package. The testpackage linter requires test files to live outside
// package cli but exempts this export_test.go filename, which is only
// compiled during tests.

var (
	ExtractLastPromptAndReply = extractLastPromptAndReply
	BuildStopMessage          = buildStopMessage
	TranscriptRaceInterval    = transcriptRaceInterval
	HashMessage               = hashMessage
	LoadDedup                 = loadDedup
	SaveDedup                 = saveDedup
	DedupPath                 = dedupPath
)

// DedupWindow mirrors dedupWindow for tests.
const DedupWindow = dedupWindow

// DedupEntry mirrors dedupEntry for tests.
type DedupEntry = dedupEntry
