package main

// Version is the running build's version string. It is overwritten at
// release-build time via -ldflags "-X main.Version=1.2.3"; local dev
// builds keep the "dev" default.
var Version = "dev"
