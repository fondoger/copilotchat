module github.com/fondoger/copilotchat

go 1.27.3

require (
	github.com/doganarif/govisual/mcp v1.0.1
	github.com/doganarif/govisual/v2 v2.0.1
	github.com/egoist/mygo v0.7.1
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/image v0.46.0
)

require (
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/google/jsonschema-go v0.3.0 // indirect
	github.com/google/pprof v0.0.0-20240227163752-401108e1b7e7 // indirect
	github.com/modelcontextprotocol/go-sdk v1.0.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// https://github.com/doganarif/GoVisual/pull/70: outbound HTTP headers and bodies.
replace github.com/doganarif/govisual/mcp => github.com/fondoger/GoVisual/mcp v1.0.2-0.20261022121931-1d5fe49983b1

replace github.com/doganarif/govisual/v2 => github.com/fondoger/GoVisual/v2 v2.0.2-0.20261022121931-1d5fe49983b1

tool github.com/egoist/mygo/cmd/mygo
