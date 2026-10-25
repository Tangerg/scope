module github.com/Tangerg/scope/tools

go 1.27.3

require (
	github.com/Tangerg/scope/core v0.57.0
	github.com/bluekeyes/go-gitdiff v0.9.0
	github.com/bmatcuk/doublestar/v4 v4.10.3
	github.com/go-resty/resty/v2 v2.17.2
	github.com/samber/lo v1.53.0
	golang.org/x/net v0.61.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/pb33f/go-yaml v0.1.1 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.2 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

retract v0.54.0 // File glob admission depended on ripgrep availability.
