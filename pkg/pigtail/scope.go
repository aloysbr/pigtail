package pigtail

// Scope isolation of the subject layer. See docs/wire-protocol.md
// §"Subject scope" for the contract.

// HeaderScope carries the caller's scope on every theme/subject/phase
// request: a scope name or alias declared in the daemon's scope table; absent
// = the table's default (fail-closed).
const HeaderScope = "X-Pigtail-Scope"

// HeaderCrossScope, set to CrossScopeWrite, lets the master caller write onto
// another scope's object. It never widens any other caller.
const HeaderCrossScope = "X-Pigtail-Cross-Scope"

// CrossScopeWrite is the only accepted value of HeaderCrossScope.
const CrossScopeWrite = "write"

// HeaderSession names the Claude session a CLI call runs for
// ($CLAUDE_CODE_SESSION_ID). Declarative: it only exempts the maintenance
// owner (the session that ran `pigtail maintenance on`) and the session named
// with `--exempt`.
const HeaderSession = "X-Pigtail-Session"
