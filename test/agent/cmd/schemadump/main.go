// Prints a full POST /v1/agent/run JSON body per phase. Embeds outputSchema
// as raw JSON bytes (no json.Marshal on the schema) so test/agent can use
// bytes.Equal against controller/agenticrun schema vars.
//
// Usage: go run ./test/agent/cmd/schemadump [execution|verification|escalation|analysis]
package main

import (
	"fmt"
	"os"

	agenticrun "github.com/openshift/lightspeed-agentic-operator/controller/agenticrun"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: schemadump [execution|verification|escalation|analysis]")
		os.Exit(2)
	}
	var schema []byte
	switch os.Args[1] {
	case "execution":
		schema = agenticrun.ExecutionOutputSchema
	case "verification":
		schema = agenticrun.VerificationOutputSchema
	case "escalation":
		schema = agenticrun.EscalationOutputSchema
	case "analysis":
		schema = []byte(`{"type":"object","properties":{"options":{"type":"array","minItems":1}}}`)
	default:
		fmt.Fprintln(os.Stderr, "unknown phase")
		os.Exit(2)
	}
	_, _ = os.Stdout.Write([]byte(`{"query":"curl-smoke","outputSchema":`))
	_, _ = os.Stdout.Write(schema)
	_, _ = os.Stdout.Write([]byte(`,"context":{"targetNamespaces":["demo-ns"]}}`))
}
