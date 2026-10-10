module e2m.local/agent

go 1.26.0

toolchain go1.26.4

require (
	e2m.local/contracts v0.0.0
	golang.org/x/sys v0.49.0
)

replace e2m.local/contracts => ../../packages/e2m-contracts
