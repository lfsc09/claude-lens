package main

// Version is overwritten at release-build time via
// -ldflags "-X main.Version=1.2.3" (see .github/workflows/deploy.yaml,
// which reads it from the `version` file bumped by
// .githooks/post-commit). Local dev builds keep the "dev" default.
//
// The linker symbol must be "main.Version", not the module's full import
// path — for a package named main, ldflags -X resolves by that literal
// package name, not by import path. Note that -X gives no error or
// warning on a mismatched path; it just silently leaves the variable at
// its source default, so a wrong path here fails quietly, not loudly.
var Version = "dev"
