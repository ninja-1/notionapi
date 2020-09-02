module github.com/ninja-1/notionapi/do

require (
	github.com/kjk/caching_http_client v0.0.0-20190810075619-06ff809674f7
	github.com/kjk/fmthtml v0.0.0-20190816041536-39f5e479d32d
	github.com/ninja-1/notionapi v0.0.0-20190816064201-86f6a8c454bb
	github.com/kjk/u v0.0.0-20191229080709-d1ac8976d53f
	github.com/kr/pretty v0.1.0 // indirect
	github.com/mattn/go-isatty v0.0.9 // indirect
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
)

replace github.com/ninja-1/notionapi => ./..

go 1.13
