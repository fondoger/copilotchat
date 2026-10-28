module github.com/fondoger/copilotchat

go 1.27.3

// Prefer use `go` tool to manage `go.mod` and `go.sum` instead of manually editing them.
require (
	github.com/doganarif/govisual/mcp v1.0.1
	github.com/doganarif/govisual/v2 v2.0.1
	github.com/egoist/mygo v0.7.2-0.20261027092812-b553a90aefc5
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/image v0.46.0
)

require (
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20240227163752-401108e1b7e7 // indirect
	github.com/modelcontextprotocol/go-sdk v1.8.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// https://github.com/doganarif/GoVisual/pull/70: outbound HTTP headers and bodies.
replace github.com/doganarif/govisual/mcp => github.com/fondoger/GoVisual/mcp v1.0.2-0.20261028054924-546a017a21d8

replace github.com/doganarif/govisual/v2 => github.com/fondoger/GoVisual/v2 v2.0.2-0.20261028054924-546a017a21d8

tool github.com/egoist/mygo/cmd/mygo
