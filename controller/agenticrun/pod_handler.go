package agenticrun

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

const msgSandboxNoResult = "sandbox exited without creating result"

// stepCondMu serializes completeStep to prevent the pod event handler
// and the timeout ticker from racing on the same step condition.
var stepCondMu sync.Mutex

// ---------------------------------------------------------------------------
// Pod event handler
// ---------------------------------------------------------------------------

// handlePodEvent is the Watches handler for sandbox pods. It evaluates
// the step FSM on every pod event and acts on the outcome:
//
//	Completed → cleanup pod+CM, enqueue reconcile (phase routing picks up Result CR)
//	Failed    → patch step condition, cleanup pod+CM
//	Running   → patch step reason (WaitingForSandbox / Running)
func (r *AgenticRunReconciler) handlePodEvent(ctx context.Context, obj client.Object) []ctrl.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	// Not a sandbox pod — ignore.
	step := pod.Labels[LabelStep]
	runName := pod.Annotations[AnnotationRunName]
	if pod.Labels[LabelRun] == "" || step == "" || runName == "" {
		return nil
	}

	// Look up the owning AgenticRun. Gone → nothing to do.
	var run agenticv1alpha1.AgenticRun
	if err := r.Get(ctx, client.ObjectKey{Name: runName, Namespace: r.Namespace}, &run); err != nil {
		return nil
	}

	// Resolve once: condition type, claim name, and enrich the logger.
	condType := stepConditionType(step)
	claimName := sandboxClaimName(&run, step)
	runUID := string(run.UID)
	ctx = logf.IntoContext(ctx, logf.FromContext(ctx).WithValues(
		LogKeyName, pod.Name, LogKeyStep, step,
		LogKeyClaim, claimName, "runUID", runUID, LogKeyCondition, condType,
	))

	// Pod still in progress — lightweight reason update, no FSM needed.
	// Edge cases (node death, force delete) are caught by the timeout ticker.
	if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodUnknown {
		reason := ReasonRunning
		if pod.Status.Phase != corev1.PodRunning {
			reason = ReasonWaitingForSandbox
		}
		r.patchStepCondition(ctx, &run, condType, metav1.ConditionUnknown, reason, "Sandbox pod "+reason)
		return nil
	}

	// Pod terminated — phase tells us the outcome.
	if err := r.completeStep(ctx, &run, pod, step, condType, ""); err != nil {
		return nil
	}
	return nil
}

// completeStep handles step completion: patches the step condition (and result ref
// on success), emits audit events, and releases the sandbox. When timeoutMsg is
// non-empty the pod phase is ignored and a timeout failure is recorded. Returns an
// error if the status patch fails — the caller should bail so the timeout loop
// can retry.
func (r *AgenticRunReconciler) completeStep(ctx context.Context, run *agenticv1alpha1.AgenticRun, pod *corev1.Pod, step, condType, timeoutMsg string) error {
	stepCondMu.Lock()
	defer stepCondMu.Unlock()

	if c := meta.FindStatusCondition(run.Status.Conditions, condType); c != nil && c.Status != metav1.ConditionUnknown {
		return nil
	}

	log := logf.FromContext(ctx)
	var resultCR client.Object
	var patchErr error

	if timeoutMsg != "" {
		log.Info("sandbox timeout", "message", timeoutMsg)
		patchErr = r.patchStepCondition(ctx, run, condType, metav1.ConditionFalse, ReasonSandboxTimeout, timeoutMsg)
	} else if pod.Status.Phase == corev1.PodSucceeded {
		var reason string
		resultCR, reason = validateResultCR(ctx, r.Client, run, step, r.Namespace)
		if resultCR != nil && reason == "" {
			stepReason := ReasonSucceeded
			stepMsg := "Sandbox completed"
			if step == "analysis" {
				if ar, ok := resultCR.(*agenticv1alpha1.AnalysisResult); ok && ar.Status.ActionRequired == agenticv1alpha1.ActionRequiredFalse {
					stepReason = reasonNoActionRequired
					stepMsg = "Analysis complete — no action required"
				}
			}
			log.Info("sandbox step succeeded", "reason", stepReason)
			patchErr = r.patchStepResult(ctx, run, step, condType, resultCR.GetName(),
				metav1.ConditionTrue, stepReason, stepMsg)
		} else if resultCR != nil {
			log.Info("agent reported failure", "reason", reason)
			if step == "verification" {
				// OLS-3817: an objective verification failure (the agent ran and
				// its VerificationResult reports the remediation did not work)
				// escalates directly instead of retrying execution or terminating.
				// Set Verified=False and Escalated=Unknown so DerivePhase routes to
				// Escalating and the reconciler dispatches handleEscalation.
				patchErr = r.patchVerificationFailedEscalating(ctx, run, resultCR.GetName(), reason)
			} else {
				patchErr = r.patchStepResult(ctx, run, step, condType, resultCR.GetName(),
					metav1.ConditionFalse, ReasonSandboxFailed, reason)
			}
		} else {
			log.Error(nil, "sandbox result validation failed", "reason", reason)
			patchErr = r.patchStepCondition(ctx, run, condType, metav1.ConditionFalse, ReasonSandboxFailed, reason)
		}
	} else {
		failMsg := podFailMessage(pod)
		log.Info("sandbox step failed", "message", failMsg)
		patchErr = r.patchStepCondition(ctx, run, condType, metav1.ConditionFalse, ReasonSandboxFailed, failMsg)
	}

	if patchErr != nil {
		return patchErr
	}

	if r.Audit != nil {
		r.Audit.CompleteStep(run, step, resultCR)
	}
	r.releaseSandbox(ctx, run, step)
	return nil
}

// patchStepCondition patches the step condition on the AgenticRun if it changed.
func (r *AgenticRunReconciler) patchStepCondition(ctx context.Context, run *agenticv1alpha1.AgenticRun, condType string, status metav1.ConditionStatus, reason, message string) error {
	if condType == "" {
		return nil
	}

	if c := meta.FindStatusCondition(run.Status.Conditions, condType); c != nil {
		if c.Status != metav1.ConditionUnknown {
			return nil
		}
		if c.Status == status && c.Reason == reason {
			return nil
		}
	}

	base := run.DeepCopy()
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: run.Generation,
	})
	if err := r.statusPatch(ctx, run, base); err != nil {
		logf.FromContext(ctx).Error(err, "pod handler: failed to patch step condition", LogKeyCondition, condType)
		return err
	}
	return nil
}

// patchStepResult patches both the result ref and the step condition in a single
// status update so the reconciler sees them atomically.
func (r *AgenticRunReconciler) patchStepResult(ctx context.Context, run *agenticv1alpha1.AgenticRun, step, condType, crName string, status metav1.ConditionStatus, reason, message string) error {
	if condType == "" {
		return nil
	}
	base := run.DeepCopy()
	outcome := agenticv1alpha1.ActionOutcomeSucceeded
	if status != metav1.ConditionTrue {
		outcome = agenticv1alpha1.ActionOutcomeFailed
	}
	appendResultRef(run, step, crName, outcome)
	aggregateTokenUsage(ctx, r.Client, run, step, crName, r.Namespace)
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: run.Generation,
	})
	if err := r.statusPatch(ctx, run, base); err != nil {
		logf.FromContext(ctx).Error(err, "pod handler: failed to patch step result", LogKeyCondition, condType)
		return err
	}
	return nil
}

// patchVerificationFailedEscalating records an objective verification failure and
// routes the run onto the escalation path (OLS-3817). It appends the verification
// result ref and, in a single status patch, sets Verified=False/VerificationFailed
// plus Escalated=Unknown/VerificationFailed so DerivePhase yields Escalating.
func (r *AgenticRunReconciler) patchVerificationFailedEscalating(ctx context.Context, run *agenticv1alpha1.AgenticRun, crName, reason string) error {
	base := run.DeepCopy()
	appendResultRef(run, "verification", crName, agenticv1alpha1.ActionOutcomeFailed)
	aggregateTokenUsage(ctx, r.Client, run, "verification", crName, r.Namespace)
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               agenticv1alpha1.AgenticRunConditionVerified,
		Status:             metav1.ConditionFalse,
		Reason:             agenticv1alpha1.ReasonVerificationFailed,
		Message:            fmt.Sprintf("Verification failed: %s", reason),
		ObservedGeneration: run.Generation,
	})
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               agenticv1alpha1.AgenticRunConditionEscalated,
		Status:             metav1.ConditionUnknown,
		Reason:             agenticv1alpha1.ReasonVerificationFailed,
		Message:            "Verification failed, escalating",
		ObservedGeneration: run.Generation,
	})
	if err := r.statusPatch(ctx, run, base); err != nil {
		logf.FromContext(ctx).Error(err, "pod handler: failed to patch verification-failed escalation")
		return err
	}
	return nil
}

// aggregateTokenUsage fetches the Result CR's tokenUsage and adds it to the
// run's cumulative total. If the Result CR has no tokenUsage the run is left
// unchanged. The run's TokenUsage is initialised on first non-zero contribution.
func aggregateTokenUsage(ctx context.Context, c client.Client, run *agenticv1alpha1.AgenticRun, step, crName, namespace string) {
	key := client.ObjectKey{Name: crName, Namespace: namespace}
	var tu agenticv1alpha1.TokenUsage

	switch step {
	case "analysis":
		cr := &agenticv1alpha1.AnalysisResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return
		}
		tu = cr.Status.TokenUsage
	case "execution":
		cr := &agenticv1alpha1.ExecutionResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return
		}
		tu = cr.Status.TokenUsage
	case "verification":
		cr := &agenticv1alpha1.VerificationResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return
		}
		tu = cr.Status.TokenUsage
	case "escalation":
		cr := &agenticv1alpha1.EscalationResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return
		}
		tu = cr.Status.TokenUsage
	default:
		return
	}

	if tu.IsZero() {
		return
	}

	if run.Status.TokenUsage.IsZero() {
		run.Status.TokenUsage = agenticv1alpha1.TokenUsage{
			InputTokens:  new(int64),
			OutputTokens: new(int64),
		}
	}
	if tu.InputTokens != nil {
		*run.Status.TokenUsage.InputTokens += *tu.InputTokens
	}
	if tu.OutputTokens != nil {
		*run.Status.TokenUsage.OutputTokens += *tu.OutputTokens
	}
}

// appendResultRef appends a StepResultRef to the run's status for the given step.
func appendResultRef(run *agenticv1alpha1.AgenticRun, step, name string, outcome agenticv1alpha1.ActionOutcome) {
	ref := agenticv1alpha1.StepResultRef{Name: name, Outcome: outcome}
	switch step {
	case "analysis":
		run.Status.Steps.Analysis.Results = append(run.Status.Steps.Analysis.Results, ref)
	case "execution":
		run.Status.Steps.Execution.Results = append(run.Status.Steps.Execution.Results, ref)
	case "verification":
		run.Status.Steps.Verification.Results = append(run.Status.Steps.Verification.Results, ref)
	case "escalation":
		run.Status.Steps.Escalation.Results = append(run.Status.Steps.Escalation.Results, ref)
	}
}

func (r *AgenticRunReconciler) releaseSandbox(ctx context.Context, run *agenticv1alpha1.AgenticRun, step string) {
	if err := r.Agent.ReleaseSandbox(ctx, run, step); err != nil {
		logf.FromContext(ctx).Error(err, "pod handler: failed to release sandbox", LogKeyStep, step)
	}
}

// ---------------------------------------------------------------------------
// Timeout ticker — background goroutine for time-driven timeout checks
// ---------------------------------------------------------------------------

const sandboxTimeoutCheckInterval = 1 * time.Minute

// runTimeoutLoop runs handleTimeEvent in a loop.
// Stopped when ctx is cancelled (manager shutdown).
func (r *AgenticRunReconciler) runTimeoutLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(sandboxTimeoutCheckInterval):
			r.handleTimeEvent(ctx)
		}
	}
}

// handleTimeEvent checks all sandbox pods for start/overall timeouts.
func (r *AgenticRunReconciler) handleTimeEvent(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("sandbox-timeout")
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(r.Namespace), client.HasLabels{LabelRun, LabelStep}); err != nil {
		log.Error(err, "failed to list sandbox pods")
		return
	}

	now := time.Now()
	for i := range pods.Items {
		pod := &pods.Items[i]
		step := pod.Labels[LabelStep]
		runName := pod.Annotations[AnnotationRunName]
		condType := stepConditionType(step)
		if runName == "" {
			continue
		}

		var run agenticv1alpha1.AgenticRun
		if err := r.Get(ctx, client.ObjectKey{Name: runName, Namespace: r.Namespace}, &run); err != nil {
			continue
		}

		// Retry: pod already terminal but step condition still pending (patch failed earlier).
		phase := pod.Status.Phase
		if (phase == corev1.PodSucceeded || phase == corev1.PodFailed) && isStepInProgress(&run, condType) {
			log.Info("retrying completion for terminal pod", LogKeyName, pod.Name, LogKeyStep, step)
			_ = r.completeStep(ctx, &run, pod, step, condType, "")
			continue
		}

		created := pod.CreationTimestamp.Time
		var message string
		if startTimedOut(phase, created, now, podStartTimeout) {
			message = fmt.Sprintf("sandbox pod did not start within %s", podStartTimeout)
		} else if overallTimedOut(created, now, stepTimeout(step)) {
			message = fmt.Sprintf("sandbox exceeded timeout %s", stepTimeout(step))
		} else {
			continue
		}

		_ = r.completeStep(ctx, &run, pod, step, condType, message)
	}
}

// ---------------------------------------------------------------------------
// Step ↔ condition / claim mapping helpers
// ---------------------------------------------------------------------------

// stepConditionType maps a step label value to the AgenticRun condition type.
func stepConditionType(step string) string {
	switch step {
	case "analysis":
		return agenticv1alpha1.AgenticRunConditionAnalyzed
	case "execution":
		return agenticv1alpha1.AgenticRunConditionExecuted
	case "verification":
		return agenticv1alpha1.AgenticRunConditionVerified
	case "escalation":
		return agenticv1alpha1.AgenticRunConditionEscalated
	default:
		return ""
	}
}

// isStepInProgress returns true if the step condition is Unknown (in-progress).
func isStepInProgress(run *agenticv1alpha1.AgenticRun, condType string) bool {
	c := meta.FindStatusCondition(run.Status.Conditions, condType)
	return c != nil && c.Status == metav1.ConditionUnknown
}

func sandboxClaimName(run *agenticv1alpha1.AgenticRun, step string) string {
	switch step {
	case "analysis":
		return run.Status.Steps.Analysis.Sandbox.ClaimName
	case "execution":
		return run.Status.Steps.Execution.Sandbox.ClaimName
	case "verification":
		return run.Status.Steps.Verification.Sandbox.ClaimName
	case "escalation":
		return run.Status.Steps.Escalation.Sandbox.ClaimName
	default:
		return ""
	}
}

// validateResultCR fetches the Result CR for the step, validates it has a
// Completed=True condition, and checks for agent failure (failureReason or
// Completed reason=Failed). Returns the CR and an empty reason on success,
// or nil/reason on any failure.
func validateResultCR(ctx context.Context, c client.Client, run *agenticv1alpha1.AgenticRun, step, namespace string) (client.Object, string) {
	name := resultCRName(run.Name, step, nextResultIndex(run, step))
	key := client.ObjectKey{Name: name, Namespace: namespace}

	obj, conditions, failureReason := fetchResultCRByStep(ctx, c, key, step)
	if obj == nil {
		return nil, msgSandboxNoResult
	}

	cond := meta.FindStatusCondition(conditions, agenticv1alpha1.ResultConditionCompleted)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		return nil, "result CR missing Completed condition"
	}
	if failureReason != "" {
		return obj, failureReason
	}
	if cond.Reason == agenticv1alpha1.ResultReasonFailed {
		return obj, "agent reported failure"
	}
	if reason := validateResultStatus(obj); reason != "" {
		return nil, reason
	}
	return obj, ""
}

// validateResultStatus checks that the Result CR has mandatory status fields
// populated for a successful result.
func validateResultStatus(obj client.Object) string {
	switch cr := obj.(type) {
	case *agenticv1alpha1.AnalysisResult:
		if cr.Status.ActionRequired != agenticv1alpha1.ActionRequiredFalse && len(cr.Status.Options) == 0 {
			return "result CR missing options"
		}
	case *agenticv1alpha1.ExecutionResult:
		if len(cr.Status.ActionsTaken) == 0 {
			return "result CR missing actionsTaken"
		}
	case *agenticv1alpha1.VerificationResult:
		if len(cr.Status.Checks) == 0 {
			return "result CR missing checks"
		}
	case *agenticv1alpha1.EscalationResult:
		if cr.Status.Summary == "" {
			return "result CR missing summary"
		}
	}
	return ""
}

func fetchResultCRByStep(ctx context.Context, c client.Client, key client.ObjectKey, step string) (client.Object, []metav1.Condition, string) {
	switch step {
	case "analysis":
		cr := &agenticv1alpha1.AnalysisResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return nil, nil, ""
		}
		return cr, cr.Status.Conditions, cr.Status.FailureReason
	case "execution":
		cr := &agenticv1alpha1.ExecutionResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return nil, nil, ""
		}
		return cr, cr.Status.Conditions, cr.Status.FailureReason
	case "verification":
		cr := &agenticv1alpha1.VerificationResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return nil, nil, ""
		}
		return cr, cr.Status.Conditions, cr.Status.FailureReason
	case "escalation":
		cr := &agenticv1alpha1.EscalationResult{}
		if err := c.Get(ctx, key, cr); err != nil {
			return nil, nil, ""
		}
		return cr, cr.Status.Conditions, cr.Status.FailureReason
	default:
		return nil, nil, ""
	}
}

// podFailMessage returns a human-readable failure description from the pod's
// first terminated container, falling back to a generic message.
func podFailMessage(pod *corev1.Pod) string {
	if pod.Status.Phase == corev1.PodSucceeded {
		return msgSandboxNoResult
	}
	msg, exitCode := podTerminatedInfo(pod)
	if msg != "" {
		return msg
	}
	if exitCode != nil {
		return fmt.Sprintf("sandbox pod failed (exit %d)", *exitCode)
	}
	return "sandbox pod failed"
}

// startTimedOut returns true if the pod has not reached Running within the start deadline.
func startTimedOut(phase corev1.PodPhase, created, now time.Time, timeout time.Duration) bool {
	if phase == corev1.PodRunning || phase == corev1.PodSucceeded || phase == corev1.PodFailed {
		return false
	}
	return now.Sub(created) > timeout
}

// overallTimedOut returns true if the pod has exceeded the step deadline.
func overallTimedOut(created, now time.Time, timeout time.Duration) bool {
	return now.Sub(created) > timeout
}

// podTerminatedInfo returns the first terminated container's message and exit code.
func podTerminatedInfo(pod *corev1.Pod) (msg string, exitCode *int32) {
	if pod == nil || len(pod.Status.ContainerStatuses) == 0 {
		return "", nil
	}
	term := pod.Status.ContainerStatuses[0].State.Terminated
	if term == nil {
		return "", nil
	}
	code := term.ExitCode
	return term.Message, &code
}
