module github.com/vaifstudio/vaif-go

go 1.26.10

require github.com/gorilla/websocket v1.5.3

// Tags v0.1.0 and v0.1.1 in this repository predate the module path
// github.com/vaifstudio/vaif-go. Their go.mod declares github.com/vaifllc/vaif-go,
// so they cannot be required under this path, and the client they contain calls
// API routes that are not served.
retract (
	v0.1.1 // Declares module path github.com/vaifllc/vaif-go; calls API routes that are not served.
	v0.1.0 // Declares module path github.com/vaifllc/vaif-go; calls API routes that are not served.
)
