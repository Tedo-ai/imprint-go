module imprint-demo

go 1.24.2

require (
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/redis/go-redis/v9 v9.7.0
	github.com/tedo-ai/imprint-go v0.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
)

// For local development, use the parent directory (SDK root)
replace github.com/tedo-ai/imprint-go => ../
