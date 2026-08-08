package core

// Version is the single source of truth for the Agent version. Both the CLI
// (`weft version`) and the API (`GET /`) read it from here, so they never
// drift.
const Version = "0.1.0"
