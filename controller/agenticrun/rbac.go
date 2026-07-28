package agenticrun

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete;get

const (
	rbacNamespacesAnnotation        = "agentic.openshift.io/rbac-namespaces"
	defaultReaderClusterRoleBinding = "lightspeed-agent-cluster-reader"

	ErrCreateExecutionSA        = "create execution SA"
	ErrCreateRole               = "create Role in"
	ErrCreateRoleBinding        = "create RoleBinding in"
	ErrCreateClusterRole        = "create ClusterRole"
	ErrCreateClusterRoleBinding = "create ClusterRoleBinding"
	ErrAddReaderSubject         = "add subject to reader ClusterRoleBinding"
	ErrRemoveReaderSubject      = "remove subject from reader ClusterRoleBinding"
	ErrDeleteRoleBinding        = "delete RoleBinding in"
	ErrDeleteRole               = "delete Role in"
	ErrDeleteClusterRoleBinding = "delete ClusterRoleBinding"
	ErrDeleteClusterRole        = "delete ClusterRole"
	ErrDeleteExecutionSA        = "delete execution SA"
)

var readerBindings atomic.Value // []string — cached CRB names; nil until first discovery

func init() {
	readerBindings.Store([]string(nil))
}

// resolveReaderBindings returns all ClusterRoleBindings that list the
// lightspeed-agent SA as a subject.  Results are discovered once and cached
// for the lifetime of the process (CRBs are static infrastructure).
// If discovery has not yet succeeded, it is triggered on demand.
func resolveReaderBindings(ctx context.Context, c client.Client, operatorNS string) ([]string, error) {
	if names := readerBindings.Load().([]string); len(names) > 0 {
		return names, nil
	}

	log := logf.FromContext(ctx)
	log.Info("discovering reader ClusterRoleBindings by SA subject")

	crbList := &rbacv1.ClusterRoleBindingList{}
	if err := c.List(ctx, crbList); err != nil {
		return nil, fmt.Errorf("list ClusterRoleBindings for reader discovery: %w", err)
	}

	var names []string
	for i := range crbList.Items {
		for _, s := range crbList.Items[i].Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && s.Name == defaultSandboxSA && s.Namespace == operatorNS {
				names = append(names, crbList.Items[i].Name)
				break
			}
		}
	}

	if len(names) == 0 {
		return nil, fmt.Errorf("no ClusterRoleBinding found with subject %s/%s — ensure reader RBAC is configured", operatorNS, defaultSandboxSA)
	}

	log.Info("resolved reader ClusterRoleBindings", "bindings", names)
	readerBindings.Store(names)
	return names, nil
}

// executionSAName returns the per-run ServiceAccount name for execution RBAC isolation.
// Uses the same truncation pattern as executionRoleName. Collision is theoretically possible
// for very long namespace+name combinations (>55 chars) that share the same prefix after
// truncation, but is near-impossible in practice with typical naming conventions.
func executionSAName(run *agenticv1alpha1.AgenticRun) string {
	return truncateK8sName(fmt.Sprintf("ls-exec-%s-%s", run.Namespace, run.Name))
}

// ensureExecutionSA creates a per-run ServiceAccount for execution RBAC isolation
// and adds it as a subject to the shared cluster-reader ClusterRoleBinding for base read access.
// No owner reference — cross-namespace owner refs are unsupported by Kubernetes GC.
// Cleanup is handled explicitly by cleanupExecutionRBAC (via finalizer on AgenticRun deletion).
func ensureExecutionSA(ctx context.Context, c client.Client, run *agenticv1alpha1.AgenticRun, operatorNS string) (string, error) {
	saName := executionSAName(run)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: operatorNS,
			Labels:    rbacLabels(run.Name, "execution-sa"),
		},
	}
	if err := c.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("%s %s: %w", ErrCreateExecutionSA, saName, err)
	}

	if err := addReaderSubject(ctx, c, saName, operatorNS); err != nil {
		return "", err
	}

	return saName, nil
}

// addReaderSubject adds the SA as a subject to every ClusterRoleBinding that
// references the lightspeed-agent SA (e.g. cluster-reader, cluster-monitoring-view).
// Idempotent — skips bindings where the subject is already present.
// Retries each binding independently on conflict (optimistic locking).
func addReaderSubject(ctx context.Context, c client.Client, saName, namespace string) error {
	names, err := resolveReaderBindings(ctx, c, namespace)
	if err != nil {
		return fmt.Errorf("%s: %w", ErrAddReaderSubject, err)
	}

	subject := rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      saName,
		Namespace: namespace,
	}

	for _, bindingName := range names {
		if err := addSubjectToBinding(ctx, c, bindingName, subject); err != nil {
			return err
		}
	}
	return nil
}

func addSubjectToBinding(ctx context.Context, c client.Client, bindingName string, subject rbacv1.Subject) error {
	for attempts := 0; attempts < 3; attempts++ {
		crb := &rbacv1.ClusterRoleBinding{}
		if err := c.Get(ctx, client.ObjectKey{Name: bindingName}, crb); err != nil {
			return fmt.Errorf("%s %s: %w", ErrAddReaderSubject, bindingName, err)
		}

		for _, s := range crb.Subjects {
			if s.Kind == subject.Kind && s.Name == subject.Name && s.Namespace == subject.Namespace {
				return nil
			}
		}

		crb.Subjects = append(crb.Subjects, subject)
		err := c.Update(ctx, crb)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("%s %s: %w", ErrAddReaderSubject, bindingName, err)
		}
	}
	return fmt.Errorf("%s: conflict after retries", ErrAddReaderSubject)
}

// removeReaderSubject removes the SA from every cached reader ClusterRoleBinding.
// Idempotent — no-op if the subject is not present. Returns nil when no bindings
// are found (they may have been deleted during teardown); propagates all other errors.
func removeReaderSubject(ctx context.Context, c client.Client, saName, namespace string) error {
	names, err := resolveReaderBindings(ctx, c, namespace)
	if err != nil {
		if strings.Contains(err.Error(), "no ClusterRoleBinding found") {
			return nil
		}
		return fmt.Errorf("%s: %w", ErrRemoveReaderSubject, err)
	}

	for _, bindingName := range names {
		if err := removeSubjectFromBinding(ctx, c, bindingName, saName, namespace); err != nil {
			return err
		}
	}
	return nil
}

func removeSubjectFromBinding(ctx context.Context, c client.Client, bindingName, saName, namespace string) error {
	for attempts := 0; attempts < 3; attempts++ {
		crb := &rbacv1.ClusterRoleBinding{}
		if err := c.Get(ctx, client.ObjectKey{Name: bindingName}, crb); err != nil {
			if apierrors.IsNotFound(err) {
				return nil // binding gone, nothing to remove
			}
			return fmt.Errorf("%s %s: %w", ErrRemoveReaderSubject, bindingName, err)
		}

		filtered := make([]rbacv1.Subject, 0, len(crb.Subjects))
		for _, s := range crb.Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && s.Name == saName && s.Namespace == namespace {
				continue
			}
			filtered = append(filtered, s)
		}

		if len(filtered) == len(crb.Subjects) {
			return nil
		}

		crb.Subjects = filtered
		err := c.Update(ctx, crb)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("%s %s: %w", ErrRemoveReaderSubject, bindingName, err)
		}
	}
	return fmt.Errorf("%s: conflict after retries", ErrRemoveReaderSubject)
}

// deleteExecutionSA explicitly deletes the per-run ServiceAccount after execution completes.
func deleteExecutionSA(ctx context.Context, c client.Client, run *agenticv1alpha1.AgenticRun, operatorNS string) error {
	saName := executionSAName(run)
	return deleteIfExists(ctx, c, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: operatorNS}})
}

// ensureExecutionRBAC creates a per-run SA, then Role+RoleBinding (namespace-scoped) and
// ClusterRole+ClusterRoleBinding (cluster-scoped) from the selected option's RBAC result.
// All bindings reference the per-run SA for isolation between concurrent AgenticRuns.
// Idempotent — skips resources that already exist.
func ensureExecutionRBAC(
	ctx context.Context,
	c client.Client,
	run *agenticv1alpha1.AgenticRun,
	rbacResult *agenticv1alpha1.RBACResult,
	operatorNS string,
) error {
	if rbacResult == nil {
		return nil
	}

	saName, err := ensureExecutionSA(ctx, c, run, operatorNS)
	if err != nil {
		return err
	}

	roleName := executionRoleName(run.Name)
	labels := rbacLabels(run.Name, "execution-rbac")

	subjects := []rbacv1.Subject{{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      saName,
		Namespace: operatorNS,
	}}

	if len(rbacResult.NamespaceScoped) > 0 {
		nsRules := rbacRulesToPolicyRules(rbacResult.NamespaceScoped)
		targetNS := rbacTargetNamespaces(run, rbacResult)

		if len(targetNS) > 0 {
			if run.Annotations == nil {
				run.Annotations = make(map[string]string)
			}
			run.Annotations[rbacNamespacesAnnotation] = strings.Join(targetNS, ",")
		}

		for _, ns := range targetNS {
			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns, Labels: labels},
				Rules:      nsRules,
			}
			if err := c.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("%s %s: %w", ErrCreateRole, ns, err)
			}
			binding := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns, Labels: labels},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName},
				Subjects:   subjects,
			}
			if err := c.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("%s %s: %w", ErrCreateRoleBinding, ns, err)
			}
		}
	}

	if len(rbacResult.ClusterScoped) > 0 {
		crName := clusterRoleName(run.Name)
		clusterRules := rbacRulesToPolicyRules(rbacResult.ClusterScoped)
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Labels: labels},
			Rules:      clusterRules,
		}
		if err := c.Create(ctx, cr); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s %s: %w", ErrCreateClusterRole, crName, err)
		}
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Labels: labels},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: crName},
			Subjects:   subjects,
		}
		if err := c.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s %s: %w", ErrCreateClusterRoleBinding, crName, err)
		}
	}

	return nil
}

// cleanupExecutionRBAC removes all RBAC resources and the per-run SA created for
// a run's execution. Uses the annotation to find namespaces (survives retry clearing Steps).
func cleanupExecutionRBAC(ctx context.Context, c client.Client, run *agenticv1alpha1.AgenticRun, operatorNS string) error {
	roleName := executionRoleName(run.Name)

	nsList := annotatedRBACNamespaces(run)

	for _, ns := range nsList {
		if err := deleteIfExists(ctx, c, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns}}); err != nil {
			return fmt.Errorf("%s %s: %w", ErrDeleteRoleBinding, ns, err)
		}
		if err := deleteIfExists(ctx, c, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns}}); err != nil {
			return fmt.Errorf("%s %s: %w", ErrDeleteRole, ns, err)
		}
	}

	crName := clusterRoleName(run.Name)
	if err := deleteIfExists(ctx, c, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: crName}}); err != nil {
		return fmt.Errorf("%s %s: %w", ErrDeleteClusterRoleBinding, crName, err)
	}
	if err := deleteIfExists(ctx, c, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: crName}}); err != nil {
		return fmt.Errorf("%s %s: %w", ErrDeleteClusterRole, crName, err)
	}

	saName := executionSAName(run)
	if err := removeReaderSubject(ctx, c, saName, operatorNS); err != nil {
		return err
	}

	if err := deleteExecutionSA(ctx, c, run, operatorNS); err != nil {
		return fmt.Errorf("%s: %w", ErrDeleteExecutionSA, err)
	}
	return nil
}

func annotatedRBACNamespaces(run *agenticv1alpha1.AgenticRun) []string {
	if run.Annotations == nil {
		return nil
	}
	val := run.Annotations[rbacNamespacesAnnotation]
	if val == "" {
		return nil
	}
	return strings.Split(val, ",")
}

func deleteIfExists(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func rbacTargetNamespaces(run *agenticv1alpha1.AgenticRun, rbacResult *agenticv1alpha1.RBACResult) []string {
	if len(run.Spec.TargetNamespaces) > 0 {
		return run.Spec.TargetNamespaces
	}
	if rbacResult == nil {
		return nil
	}
	seen := make(map[string]bool)
	var nsList []string
	for _, rule := range rbacResult.NamespaceScoped {
		if rule.Namespace != "" && !seen[rule.Namespace] {
			nsList = append(nsList, rule.Namespace)
			seen[rule.Namespace] = true
		}
	}
	return nsList
}

func truncateK8sName(name string) string {
	return truncateK8sNameWithBudget(name, 0)
}

func truncateK8sNameWithBudget(name string, reserved int) string {
	maxLen := 63 - reserved
	if len(name) > maxLen {
		name = strings.TrimRight(name[:maxLen], "-._")
	}
	return name
}

func executionRoleName(agenticRunName string) string {
	return truncateK8sName("ls-exec-" + agenticRunName)
}

func clusterRoleName(agenticRunName string) string {
	return truncateK8sName("ls-exec-cluster-" + agenticRunName)
}

func rbacLabels(agenticRunName, component string) map[string]string {
	return map[string]string{
		LabelRun:       truncateK8sName(agenticRunName),
		LabelComponent: component,
	}
}

func rbacRulesToPolicyRules(rules []agenticv1alpha1.RBACRule) []rbacv1.PolicyRule {
	out := make([]rbacv1.PolicyRule, len(rules))
	for i, r := range rules {
		out[i] = rbacv1.PolicyRule{
			APIGroups:     normalizeCoreAPIGroup(r.APIGroups),
			Resources:     r.Resources,
			ResourceNames: r.ResourceNames,
			Verbs:         r.Verbs,
		}
	}
	return out
}

// normalizeCoreAPIGroup maps "core" to "" for the Kubernetes core API group.
// The output schema requires minLength=1 so the LLM uses "core" instead of "".
func normalizeCoreAPIGroup(groups []string) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		if g == "core" {
			out[i] = ""
		} else {
			out[i] = g
		}
	}
	return out
}
