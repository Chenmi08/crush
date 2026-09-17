package tools

// AgentToolName is the name of the sub-agent delegation tool.
//
// It lives here, beside the other tool names, so the protocol layer can
// alias it without importing package agent (which would be an import
// cycle).
const AgentToolName = "agent"
