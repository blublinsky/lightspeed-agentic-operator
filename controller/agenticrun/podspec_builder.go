package agenticrun

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"github.com/openshift/lightspeed-agentic-operator/pkg/configuration"
)

const (
	ErrBuildBasePodSpec       = "base PodSpec with at least one container is required"
	ErrBuildAgentRequired     = "agent is required"
	ErrBuildLLMRequired       = "LLMProvider is required"
	ErrBuildSARequired        = "serviceAccount is required"
	ErrBuildMCPServers        = "build MCP servers"
	ErrMarshalMCPServerConfig = "marshal MCP server config"

	llmCredsMountPath   = "/var/run/secrets/llm-credentials"
	llmCredsVolumeName  = "llm-credentials"
	mcpHeadersMountRoot = "/var/secrets/mcp"
	mcpServersEnvVar    = "LIGHTSPEED_MCP_SERVERS"

	LabelManaged      = "agentic.openshift.io/managed"
	LabelBaseTemplate = "agentic.openshift.io/base-template"
	LabelStep         = "agentic.openshift.io/step"
	LabelAgent        = "agentic.openshift.io/agent"
	LabelRun          = "agentic.openshift.io/run"
	LabelComponent    = "agentic.openshift.io/component"
)

type mcpServerEnvEntry struct {
	Name    string              `json:"name"`
	URL     string              `json:"url"`
	Timeout int32               `json:"timeout,omitempty"`
	Headers []mcpHeaderEnvEntry `json:"headers,omitempty"`
}

type mcpHeaderEnvEntry struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	SecretName string `json:"secretName,omitempty"`
}

// PodSpecBuilder overlays agent-specific configuration (env vars, volumes,
// probes) onto a base PodSpec provided by the configuration cache.
type PodSpecBuilder struct{}

// Build takes a base PodSpec (from the configuration cache) and overlays
// agent, LLM, tools, and OTEL configuration for the given step.
// The base PodSpec must contain at least one container (the agent container).
func (b *PodSpecBuilder) Build(
	base *corev1.PodSpec,
	agent *agenticv1alpha1.Agent,
	llm *agenticv1alpha1.LLMProvider,
	tools *agenticv1alpha1.ToolsSpec,
	otelCfg *configuration.OTELConfig,
	step string,
	runUID string,
	serviceAccount string,
) (*corev1.PodSpec, error) {
	if base == nil || len(base.Containers) == 0 {
		return nil, fmt.Errorf("%s", ErrBuildBasePodSpec)
	}
	if agent == nil {
		return nil, fmt.Errorf("%s", ErrBuildAgentRequired)
	}
	if llm == nil {
		return nil, fmt.Errorf("%s", ErrBuildLLMRequired)
	}
	if serviceAccount == "" {
		return nil, fmt.Errorf("%s", ErrBuildSARequired)
	}

	podSpec := base.DeepCopy()
	podSpec.ServiceAccountName = serviceAccount
	podSpec.AutomountServiceAccountToken = ptr.To(true)

	container := &podSpec.Containers[0]
	var volumes []corev1.Volume

	container.Env = append(container.Env,
		corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER", Value: providerTypeString(llm.Spec.Type)},
		corev1.EnvVar{Name: "LIGHTSPEED_MODEL", Value: agent.Spec.Model},
	)
	b.addProviderSpecificEnv(container, llm)

	if len(agent.Spec.ReasoningConfig) > 0 {
		rcJSON, err := json.Marshal(agent.Spec.ReasoningConfig)
		if err != nil {
			return nil, fmt.Errorf("marshal reasoningConfig: %w", err)
		}
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "LIGHTSPEED_REASONING_CONFIG",
			Value: string(rcJSON),
		})
	}

	secretName := credentialsSecretName(llm)
	container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
		},
	})
	volumes = append(volumes, corev1.Volume{
		Name: llmCredsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName},
		},
	})
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      llmCredsVolumeName,
		MountPath: llmCredsMountPath,
		ReadOnly:  true,
	})

	container.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8080)},
		},
		InitialDelaySeconds: 3,
		PeriodSeconds:       10,
		FailureThreshold:    3,
	}
	container.LivenessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(8080)},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       30,
		FailureThreshold:    3,
	}

	if tools != nil {
		skillVols, skillMounts := b.buildSkills(tools.Skills)
		volumes = append(volumes, skillVols...)
		container.VolumeMounts = append(container.VolumeMounts, skillMounts...)
	}

	if tools != nil && len(tools.MCPServers) > 0 {
		mcpVols, mcpMounts, mcpEnv, err := b.buildMCPServers(tools.MCPServers)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ErrBuildMCPServers, err)
		}
		volumes = append(volumes, mcpVols...)
		container.VolumeMounts = append(container.VolumeMounts, mcpMounts...)
		container.Env = append(container.Env, mcpEnv...)
	}

	if tools != nil && len(tools.RequiredSecrets) > 0 {
		secVols, secMounts, secEnv := b.buildRequiredSecrets(tools.RequiredSecrets)
		volumes = append(volumes, secVols...)
		container.VolumeMounts = append(container.VolumeMounts, secMounts...)
		container.Env = append(container.Env, secEnv...)
	}

	appendOTELEnvVars(container, &volumes, otelCfg, runUID, step)

	podSpec.Volumes = append(podSpec.Volumes, volumes...)

	appendAuditEnvVars(container)

	return podSpec, nil
}

func appendAuditEnvVars(container *corev1.Container) {
	container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_AUDIT_ENABLED", Value: "true"})
}

const (
	otelCAVolumeName = "otel-ca"
	otelCAMountPath  = "/var/run/secrets/otel-ca"
	otelCASecretKey  = "otel-ca.crt"
)

func appendOTELEnvVars(container *corev1.Container, volumes *[]corev1.Volume, otelCfg *configuration.OTELConfig, runUID, step string) {
	if otelCfg == nil || otelCfg.CollectorEndpoint == "" {
		return
	}
	container.Env = append(container.Env,
		corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: otelCfg.CollectorEndpoint},
		corev1.EnvVar{Name: "LIGHTSPEED_AGENTICRUN_UID", Value: runUID},
		corev1.EnvVar{Name: "LIGHTSPEED_AGENTICRUN_STEP", Value: step},
	)

	if otelCfg.CASecretName != "" {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_CERTIFICATE", Value: otelCAMountPath + "/" + otelCASecretKey},
		)
		*volumes = append(*volumes, corev1.Volume{
			Name: otelCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: otelCfg.CASecretName},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      otelCAVolumeName,
			MountPath: otelCAMountPath,
			ReadOnly:  true,
		})
	}
}

func (b *PodSpecBuilder) addProviderSpecificEnv(container *corev1.Container, llm *agenticv1alpha1.LLMProvider) {
	switch llm.Spec.Type {
	case agenticv1alpha1.LLMProviderAnthropic:
		if u := providerURL(llm); u != "" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_URL", Value: u})
		}
	case agenticv1alpha1.LLMProviderGoogleCloudVertex:
		cfg := llm.Spec.GoogleCloudVertex
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "LIGHTSPEED_MODEL_PROVIDER", Value: strings.ToLower(string(cfg.ModelProvider))},
			corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_PROJECT", Value: cfg.ProjectID},
			corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_REGION", Value: cfg.Region},
		)
		if u := providerURL(llm); u != "" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_URL", Value: u})
		}
	case agenticv1alpha1.LLMProviderOpenAI:
		if u := providerURL(llm); u != "" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_URL", Value: u})
		}
	case agenticv1alpha1.LLMProviderAzureOpenAI:
		cfg := llm.Spec.AzureOpenAI
		providerURLValue := cfg.Endpoint
		if u := cfg.URL; u != "" {
			providerURLValue = u
		}
		container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_URL", Value: providerURLValue})
		if cfg.APIVersion != "" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_API_VERSION", Value: cfg.APIVersion})
		}
	case agenticv1alpha1.LLMProviderAWSBedrock:
		cfg := llm.Spec.AWSBedrock
		container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_REGION", Value: cfg.Region})
		if u := providerURL(llm); u != "" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "LIGHTSPEED_PROVIDER_URL", Value: u})
		}
	}
}

func (b *PodSpecBuilder) buildSkills(skills []agenticv1alpha1.SkillsSource) ([]corev1.Volume, []corev1.VolumeMount) {
	if len(skills) == 0 || skills[0].Image == "" {
		return nil, nil
	}
	s := skills[0]

	vol := corev1.Volume{
		Name: "skills",
		VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{
				Reference:  s.Image,
				PullPolicy: corev1.PullAlways,
			},
		},
	}
	workdirVol := corev1.Volume{
		Name:         "skills-workdir",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}

	mounts := []corev1.VolumeMount{{
		Name:      "skills-workdir",
		MountPath: "/app/skills/.agents",
	}}
	if len(s.Paths) > 0 {
		baseMountPath := "/app/skills"
		for _, p := range s.Paths {
			subPath := strings.TrimPrefix(p, "/")
			skillName := path.Base(p)
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "skills",
				MountPath: path.Join(baseMountPath, skillName),
				SubPath:   subPath,
				ReadOnly:  true,
			})
		}
	}

	return []corev1.Volume{vol, workdirVol}, mounts
}

func (b *PodSpecBuilder) buildMCPServers(servers []agenticv1alpha1.MCPServerConfig) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar, error) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	entries := make([]mcpServerEnvEntry, 0, len(servers))
	for _, s := range servers {
		entry := mcpServerEnvEntry{
			Name:    s.Name,
			URL:     s.URL,
			Timeout: s.TimeoutSeconds,
		}
		for _, h := range s.Headers {
			he := mcpHeaderEnvEntry{
				Name:   h.Name,
				Source: string(h.ValueFrom.Type),
			}
			if h.ValueFrom.Type == agenticv1alpha1.MCPHeaderSourceTypeSecret {
				he.SecretName = h.ValueFrom.Secret.Name
				volName := "mcp-header-" + h.ValueFrom.Secret.Name
				volumes = append(volumes, corev1.Volume{
					Name: volName,
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: h.ValueFrom.Secret.Name},
					},
				})
				mounts = append(mounts, corev1.VolumeMount{
					Name:      volName,
					MountPath: mcpHeadersMountRoot + "/" + h.ValueFrom.Secret.Name,
					ReadOnly:  true,
				})
			}
			entry.Headers = append(entry.Headers, he)
		}
		entries = append(entries, entry)
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", ErrMarshalMCPServerConfig, err)
	}

	envs := []corev1.EnvVar{{Name: mcpServersEnvVar, Value: string(data)}}
	return volumes, mounts, envs, nil
}

func (b *PodSpecBuilder) buildRequiredSecrets(secrets []agenticv1alpha1.SecretRequirement) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	var envs []corev1.EnvVar

	for _, s := range secrets {
		switch s.MountAs.Type {
		case agenticv1alpha1.SecretMountFilePath:
			volName := "req-" + s.Name
			volumes = append(volumes, corev1.Volume{
				Name: volName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: s.Name},
				},
			})
			mounts = append(mounts, corev1.VolumeMount{
				Name:      volName,
				MountPath: s.MountAs.FilePath.Path,
				ReadOnly:  true,
			})
		case agenticv1alpha1.SecretMountEnvVar:
			optional := true
			envs = append(envs, corev1.EnvVar{
				Name: s.MountAs.EnvVar.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: s.Name},
						Key:                  "token",
						Optional:             &optional,
					},
				},
			})
		}
	}
	return volumes, mounts, envs
}

func credentialsSecretName(llm *agenticv1alpha1.LLMProvider) string {
	switch llm.Spec.Type {
	case agenticv1alpha1.LLMProviderAnthropic:
		return llm.Spec.Anthropic.CredentialsSecret.Name
	case agenticv1alpha1.LLMProviderGoogleCloudVertex:
		return llm.Spec.GoogleCloudVertex.CredentialsSecret.Name
	case agenticv1alpha1.LLMProviderOpenAI:
		return llm.Spec.OpenAI.CredentialsSecret.Name
	case agenticv1alpha1.LLMProviderAzureOpenAI:
		return llm.Spec.AzureOpenAI.CredentialsSecret.Name
	case agenticv1alpha1.LLMProviderAWSBedrock:
		return llm.Spec.AWSBedrock.CredentialsSecret.Name
	default:
		return ""
	}
}

func providerURL(llm *agenticv1alpha1.LLMProvider) string {
	switch llm.Spec.Type {
	case agenticv1alpha1.LLMProviderAnthropic:
		return llm.Spec.Anthropic.URL
	case agenticv1alpha1.LLMProviderGoogleCloudVertex:
		return llm.Spec.GoogleCloudVertex.URL
	case agenticv1alpha1.LLMProviderOpenAI:
		return llm.Spec.OpenAI.URL
	case agenticv1alpha1.LLMProviderAzureOpenAI:
		return llm.Spec.AzureOpenAI.URL
	case agenticv1alpha1.LLMProviderAWSBedrock:
		return llm.Spec.AWSBedrock.URL
	default:
		return ""
	}
}

func providerTypeString(t agenticv1alpha1.LLMProviderType) string {
	switch t {
	case agenticv1alpha1.LLMProviderAnthropic:
		return "anthropic"
	case agenticv1alpha1.LLMProviderGoogleCloudVertex:
		return "vertex"
	case agenticv1alpha1.LLMProviderOpenAI:
		return "openai"
	case agenticv1alpha1.LLMProviderAzureOpenAI:
		return "azure"
	case agenticv1alpha1.LLMProviderAWSBedrock:
		return "bedrock"
	default:
		return strings.ToLower(string(t))
	}
}
