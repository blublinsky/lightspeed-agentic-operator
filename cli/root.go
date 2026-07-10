package cli

import (
	"github.com/openshift/lightspeed-agentic-operator/cli/run"
	"github.com/openshift/lightspeed-agentic-operator/cli/system"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func NewRootCmd(streams genericclioptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "oc-agentic",
		Short:        "CLI for OpenShift Agentic runs",
		Long:         "Manage AgenticRun resources — create, list, approve, watch, and inspect AI-driven agentic runs.",
		SilenceUsage: true,
	}

	cmd.AddCommand(run.NewAgenticRunCmd(streams))
	cmd.AddCommand(system.NewStatusCmd(streams))
	cmd.AddCommand(system.NewSuspendCmd(streams))
	cmd.AddCommand(system.NewResumeCmd(streams))
	cmd.AddCommand(NewVersionCmd(streams))

	return cmd
}
