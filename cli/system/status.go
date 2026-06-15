package system

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type StatusOptions struct {
	configFlags *genericclioptions.ConfigFlags
	client      client.Client
	genericclioptions.IOStreams
}

func NewStatusCmd(streams genericclioptions.IOStreams) *cobra.Command {
	o := &StatusOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show system status",
		Example: `  # Check if agentic system is suspended
  oc agentic status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.configFlags.AddFlags(cmd.Flags())
	return cmd
}

func (o *StatusOptions) Complete() error {
	var err error
	o.client, err = newClient(o.configFlags)
	return err
}

func (o *StatusOptions) Run(ctx context.Context) error {
	cfg, err := getConfig(ctx, o.client)
	if apierrors.IsNotFound(err) {
		fmt.Fprintln(o.Out, "Agentic System: Active")
		return nil
	}
	if isNoMatchError(err) {
		return fmt.Errorf("agentic system is not installed (AgenticOLSConfig CRD not found)")
	}
	if err != nil {
		return fmt.Errorf("failed to get AgenticOLSConfig: %w", err)
	}

	if cfg.Spec.Suspended {
		fmt.Fprintln(o.Out, "Agentic System: SUSPENDED")
	} else {
		fmt.Fprintln(o.Out, "Agentic System: Active")
	}
	return nil
}
