package system

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ResumeOptions struct {
	configFlags *genericclioptions.ConfigFlags
	client      client.Client
	genericclioptions.IOStreams
}

func NewResumeCmd(streams genericclioptions.IOStreams) *cobra.Command {
	o := &ResumeOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}

	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume agentic operations",
		Example: `  # Resume agentic system
  oc agentic resume`,
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

func (o *ResumeOptions) Complete() error {
	var err error
	o.client, err = newClient(o.configFlags)
	return err
}

func (o *ResumeOptions) Run(ctx context.Context) error {
	cfg, err := getConfig(ctx, o.client)
	if isNoMatchError(err) {
		return fmt.Errorf("agentic system is not installed (AgenticOLSConfig CRD not found)")
	}
	if apierrors.IsNotFound(err) {
		fmt.Fprintln(o.Out, "Agentic system is not suspended (no AgenticOLSConfig found).")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get AgenticOLSConfig: %w", err)
	}

	if !cfg.Spec.Suspended {
		fmt.Fprintln(o.Out, "Agentic system is not suspended.")
		return nil
	}

	raw := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"suspended":false}}`))
	if err := o.client.Patch(ctx, cfg, raw); err != nil {
		return fmt.Errorf("failed to patch AgenticOLSConfig: %w", err)
	}

	fmt.Fprintln(o.Out, "Agentic system resumed.")
	return nil
}
