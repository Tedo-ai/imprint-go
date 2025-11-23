module imprint-demo

go 1.24.2

require (
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/tedo-ai/imprint-go v0.0.0
)

// For local development, use the parent directory (SDK root)
replace github.com/tedo-ai/imprint-go => ../
