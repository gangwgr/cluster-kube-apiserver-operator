package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	operatorclientset "github.com/openshift/client-go/operator/clientset/versioned"
	"github.com/openshift/cluster-kube-apiserver-operator/pkg/operator/operatorclient"
	test "github.com/openshift/cluster-kube-apiserver-operator/test/library"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

var _ = g.Describe("[Jira:kube-apiserver][sig-api-machinery][FeatureGate:EventTTL] Event TTL Configuration", func() {
	var (
		kubeClient     *kubernetes.Clientset
		configClient   *configclient.Clientset
		operatorClient *operatorclientset.Clientset
		ctx            context.Context
	)

	g.BeforeEach(func() {
		ctx = context.TODO()
		kubeConfig, err := test.NewClientConfigForTest()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get kube config")

		kubeClient, err = kubernetes.NewForConfig(kubeConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		configClient, err = configclient.NewForConfig(kubeConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

		operatorClient, err = operatorclientset.NewForConfig(kubeConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create operator client")
	})

	// Loop to create separate test cases for each TTL value (5m, 10m, 15m)
	testValues := []int32{5, 10, 15}

	for _, ttlMinutes := range testValues {
		// Capture the variable for the closure
		ttl := ttlMinutes

		g.It(fmt.Sprintf("should configure and validate eventTTLMinutes=%dm [Suite:openshift/cluster-kube-apiserver-operator/conformance/serial][Disruptive][Slow]", ttl), func() {
			startTime := time.Now()
			g.By(fmt.Sprintf("=== Starting test for eventTTLMinutes=%d at %s ===", ttl, startTime.Format(time.RFC3339)))

			// Step 1: Check if EventTTL feature gate exists and get its status
			g.By("Step 1: Checking if EventTTL feature gate is present")

			featureGate, err := configClient.ConfigV1().FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get feature gate")
			g.By(fmt.Sprintf("  Current FeatureSet: %s", featureGate.Spec.FeatureSet))

			// Check if EventTTL feature gate exists (enabled or disabled)
			foundFeature := false
			isEnabled := false

			for _, featureGateDetails := range featureGate.Status.FeatureGates {
				// Check enabled features
				for _, enabledFeature := range featureGateDetails.Enabled {
					if string(enabledFeature.Name) == "EventTTL" {
						foundFeature = true
						isEnabled = true
						g.By("✓ EventTTL feature gate found and is already enabled")
						break
					}
				}
				// Check disabled features
				if !foundFeature {
					for _, disabledFeature := range featureGateDetails.Disabled {
						if string(disabledFeature.Name) == "EventTTL" {
							foundFeature = true
							isEnabled = false
							g.By("✓ EventTTL feature gate found but is disabled")
							break
						}
					}
				}
				if foundFeature {
					break
				}
			}

			// If feature gate not found, skip the test
			if !foundFeature {
				g.Skip("EventTTL feature gate not found in this cluster version")
			}

			// Enable feature gate if not already enabled
			if !isEnabled {
				g.By("Step 1b: Enabling EventTTL feature gate...")
				enableStartTime := time.Now()
				g.By(fmt.Sprintf("  Enabling at: %s", enableStartTime.Format(time.RFC3339)))

				patchData := map[string]interface{}{
					"spec": map[string]interface{}{
						"featureSet": "CustomNoUpgrade",
						"customNoUpgrade": map[string]interface{}{
							"enabled": []string{"EventTTL"},
						},
					},
				}
				patchBytes, err := json.Marshal(patchData)
				o.Expect(err).NotTo(o.HaveOccurred())
				g.By(fmt.Sprintf("  Patch data: %s", string(patchBytes)))

				_, err = configClient.ConfigV1().FeatureGates().Patch(ctx, "cluster", types.MergePatchType, patchBytes, metav1.PatchOptions{})
				o.Expect(err).NotTo(o.HaveOccurred())
				g.By("✓ EventTTL feature gate enabled - waiting 5 minutes for it to apply...")
				time.Sleep(5 * time.Minute)
				g.By(fmt.Sprintf("  Feature gate enable took: %v", time.Since(enableStartTime)))
			}

			// Step 2: Configure eventTTLMinutes
			g.By(fmt.Sprintf("\nStep 2: Configuring eventTTLMinutes=%d in KubeAPIServer CR", ttl))
			configStartTime := time.Now()

			patchData := map[string]interface{}{
				"spec": map[string]interface{}{
					"eventTTLMinutes": ttl,
				},
			}
			patchBytes, err := json.Marshal(patchData)
			o.Expect(err).NotTo(o.HaveOccurred())
			g.By(fmt.Sprintf("  Patch data: %s", string(patchBytes)))

			_, err = operatorClient.OperatorV1().KubeAPIServers().Patch(ctx, "cluster", types.MergePatchType, patchBytes, metav1.PatchOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			g.By(fmt.Sprintf("✓ eventTTLMinutes=%d configured at %s", ttl, time.Now().Format(time.RFC3339)))

			// Step 3: Wait for rollout
			g.By("Step 3: Waiting for new revision to roll out (timeout: 20 minutes)...")
			rolloutStartTime := time.Now()
			g.By(fmt.Sprintf("  Rollout started at: %s", rolloutStartTime.Format(time.RFC3339)))

			err = waitForAPIServerRollout(ctx, kubeClient, 20*time.Minute)
			o.Expect(err).NotTo(o.HaveOccurred())
			rolloutDuration := time.Since(rolloutStartTime)
			g.By(fmt.Sprintf("✓ New revision rolled out successfully in %v", rolloutDuration))
			g.By(fmt.Sprintf("  Configuration took: %v total", time.Since(configStartTime)))

			// Step 4: Verify configuration in pods
			g.By(fmt.Sprintf("Step 4: Verifying event-ttl=%dm in kube-apiserver pods", ttl))
			verifyEventTTLInPods(ctx, kubeClient, ttl)
			g.By(fmt.Sprintf("✓ eventTTLMinutes=%d verified in all running pods", ttl))

			// Step 5: Validate actual event expiration
			// IMPORTANT: We create a NEW event AFTER the configuration is applied.
			// The EventTTL feature only affects NEW events created after --event-ttl is set.
			// Existing events before the change keep their original TTL (default 3h).
			g.By(fmt.Sprintf("\nStep 5: Validating that events actually expire after %d minutes", ttl))
			eventStartTime := time.Now()

			testNamespace := "default"
			eventName := fmt.Sprintf("event-ttl-test-%dm-%d", ttl, time.Now().Unix())

			g.By(fmt.Sprintf("Creating NEW test event: %s in namespace: %s", eventName, testNamespace))
			g.By("  (This event should expire after the configured TTL, not the default 3h)")
			g.By(fmt.Sprintf("  Event creation time: %s", eventStartTime.Format(time.RFC3339)))
			testEvent := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      eventName,
					Namespace: testNamespace,
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: testNamespace,
					Name:      fmt.Sprintf("test-pod-%dm", ttl),
					UID:       types.UID(fmt.Sprintf("uid-%d", time.Now().Unix())),
				},
				Reason:         "EventTTLTest",
				Message:        fmt.Sprintf("Test event - should expire after %dm", ttl),
				Type:           corev1.EventTypeNormal,
				Source:         corev1.EventSource{Component: "event-ttl-test"},
				FirstTimestamp: metav1.Now(),
				LastTimestamp:  metav1.Now(),
				Count:          1,
			}

			createdEvent, err := kubeClient.CoreV1().Events(testNamespace).Create(ctx, testEvent, metav1.CreateOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			creationTime := createdEvent.CreationTimestamp.Time
			g.By(fmt.Sprintf("✓ Event created at: %s", creationTime.Format(time.RFC3339)))

			// Verify event exists
			event, err := kubeClient.CoreV1().Events(testNamespace).Get(ctx, eventName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			g.By(fmt.Sprintf("✓ Event confirmed to exist (UID: %s)", event.UID))
			g.By(fmt.Sprintf("  Event CreationTimestamp: %s", event.CreationTimestamp.Format(time.RFC3339)))

			// Wait for TTL + buffer
			waitDuration := time.Duration(ttl+2) * time.Minute
			expectedExpirationTime := creationTime.Add(waitDuration)
			g.By(fmt.Sprintf("Waiting %d minutes for event to expire (expected expiration: %s)...",
				int(ttl+2), expectedExpirationTime.Format(time.RFC3339)))

			// Log progress every minute
			ticker := time.NewTicker(1 * time.Minute)
			done := make(chan bool)
			elapsed := 0

			go func() {
				for {
					select {
					case <-done:
						ticker.Stop()
						return
					case <-ticker.C:
						elapsed++
						g.By(fmt.Sprintf("  ... %d/%d minutes elapsed", elapsed, int(ttl+2)))
					}
				}
			}()

			time.Sleep(waitDuration)
			done <- true

			// Verify event is deleted
			actualExpirationTime := time.Now()
			_, err = kubeClient.CoreV1().Events(testNamespace).Get(ctx, eventName, metav1.GetOptions{})
			o.Expect(err).To(o.HaveOccurred(), "event should be deleted after TTL")
			o.Expect(err.Error()).To(o.ContainSubstring("not found"), "event should return 'not found' error")

			actualTTL := actualExpirationTime.Sub(creationTime)
			g.By(fmt.Sprintf("✓ Event expired and deleted after approximately %v", actualTTL.Round(time.Minute)))
			g.By(fmt.Sprintf("  Expected TTL: %dm, Actual TTL: %v", ttl, actualTTL.Round(time.Minute)))

			totalTestDuration := time.Since(startTime)
			g.By(fmt.Sprintf("\n✅ All steps completed successfully for eventTTLMinutes=%d", ttl))
			g.By(fmt.Sprintf("  Total test duration: %v", totalTestDuration))
		})
	}

	// Rollback test: Remove configuration and disable feature gate
	g.It("should rollback to default after disabling EventTTL feature gate [Suite:openshift/cluster-kube-apiserver-operator/conformance/serial][Disruptive][Slow]", func() {
		startTime := time.Now()
		g.By(fmt.Sprintf("=== Starting rollback test at %s ===", startTime.Format(time.RFC3339)))

		// Step 1: Remove eventTTLMinutes
		g.By("Step 1: Removing eventTTLMinutes from KubeAPIServer CR")
		patchData := `{"spec":{"eventTTLMinutes":null}}`
		g.By(fmt.Sprintf("  Patch data: %s", patchData))

		_, err := operatorClient.OperatorV1().KubeAPIServers().Patch(ctx, "cluster", types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		g.By(fmt.Sprintf("✓ eventTTLMinutes removed at %s", time.Now().Format(time.RFC3339)))

		// Step 2: Disable feature gate
		g.By("\nStep 2: Disabling EventTTL feature gate")
		disableStartTime := time.Now()

		patchData2 := map[string]interface{}{
			"spec": map[string]interface{}{
				"featureSet": "Default",
			},
		}
		patchBytes, err := json.Marshal(patchData2)
		o.Expect(err).NotTo(o.HaveOccurred())
		g.By(fmt.Sprintf("  Patch data: %s", string(patchBytes)))

		_, err = configClient.ConfigV1().FeatureGates().Patch(ctx, "cluster", types.MergePatchType, patchBytes, metav1.PatchOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		g.By(fmt.Sprintf("✓ Feature gate disabled at %s", time.Now().Format(time.RFC3339)))
		g.By("  Waiting 10 minutes for rollback to complete...")

		// Log progress every 2 minutes
		ticker := time.NewTicker(2 * time.Minute)
		done := make(chan bool)
		elapsed := 0

		go func() {
			for {
				select {
				case <-done:
					ticker.Stop()
					return
				case <-ticker.C:
					elapsed += 2
					g.By(fmt.Sprintf("    ... %d/10 minutes elapsed", elapsed))
				}
			}
		}()

		time.Sleep(10 * time.Minute)
		done <- true

		g.By(fmt.Sprintf("  Disable took: %v", time.Since(disableStartTime)))

		// Step 3: Verify default configuration
		g.By("\nStep 3: Verifying default event-ttl configuration")
		verifyStartTime := time.Now()

		pods, err := kubeClient.CoreV1().Pods(operatorclient.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=openshift-kube-apiserver,apiserver=true",
		})
		o.Expect(err).NotTo(o.HaveOccurred())
		g.By(fmt.Sprintf("  Found %d total pods", len(pods.Items)))

		runningPodCount := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPodCount++
			}
		}
		g.By(fmt.Sprintf("  %d pods in Running state", runningPodCount))

		foundDefault := false
		var foundValues []string
		checkedPods := 0

		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				g.By(fmt.Sprintf("  Skipping pod %s (Phase: %s)", pod.Name, pod.Status.Phase))
				continue
			}

			checkedPods++
			g.By(fmt.Sprintf("  Checking pod %s", pod.Name))

			for _, container := range pod.Spec.Containers {
				if container.Name == "kube-apiserver" {
					allArgs := append(container.Command, container.Args...)
					for _, arg := range allArgs {
						if strings.HasPrefix(arg, "--event-ttl=") {
							ttlValue := strings.TrimPrefix(arg, "--event-ttl=")
							foundValues = append(foundValues, ttlValue)

							// Default is 3h (OpenShift) or 1h (Kubernetes)
							if ttlValue == "3h" || ttlValue == "3h0m0s" || ttlValue == "1h" || ttlValue == "1h0m0s" {
								foundDefault = true
								g.By(fmt.Sprintf("    ✓ Pod %s rolled back to default TTL: %s", pod.Name, ttlValue))
							} else {
								g.By(fmt.Sprintf("    ⚠ Pod %s has TTL: %s (not default)", pod.Name, ttlValue))
							}
							break
						}
					}
				}
			}
		}

		g.By(fmt.Sprintf("  Summary: Checked %d running pods", checkedPods))
		g.By("  Expected: 3h or 1h (default)")
		g.By(fmt.Sprintf("  Found values: %v", foundValues))
		g.By(fmt.Sprintf("  Verification took: %v", time.Since(verifyStartTime)))

		o.Expect(foundDefault).To(o.BeTrue(), fmt.Sprintf("should return to default event-ttl, but found: %v", foundValues))

		totalTestDuration := time.Since(startTime)
		g.By("\n✅ Rollback test completed successfully")
		g.By(fmt.Sprintf("  Total test duration: %v", totalTestDuration))
	})
})

// Helper functions

func waitForAPIServerRollout(ctx context.Context, kubeClient *kubernetes.Clientset, timeout time.Duration) error {
	attempt := 0
	lastPodCount := 0
	lastNotRunningCount := 0

	return wait.Poll(15*time.Second, timeout, func() (bool, error) {
		attempt++
		pods, err := kubeClient.CoreV1().Pods(operatorclient.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=openshift-kube-apiserver,apiserver=true",
		})
		if err != nil {
			g.By(fmt.Sprintf("  [Attempt %d] Error listing pods: %v", attempt, err))
			return false, nil
		}

		if len(pods.Items) == 0 {
			g.By(fmt.Sprintf("  [Attempt %d] No kube-apiserver pods found yet", attempt))
			return false, nil
		}

		notRunningCount := 0
		var notRunningPods []string
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				notRunningCount++
				notRunningPods = append(notRunningPods, fmt.Sprintf("%s (%s)", pod.Name, pod.Status.Phase))
			}
		}

		// Log only when state changes or every 4th attempt (1 minute)
		if notRunningCount != lastNotRunningCount || len(pods.Items) != lastPodCount || attempt%4 == 0 {
			if notRunningCount > 0 {
				g.By(fmt.Sprintf("  [Attempt %d] %d/%d pods running. Not running: %v",
					attempt, len(pods.Items)-notRunningCount, len(pods.Items), notRunningPods))
			} else {
				g.By(fmt.Sprintf("  [Attempt %d] All %d pods are running", attempt, len(pods.Items)))
			}
			lastPodCount = len(pods.Items)
			lastNotRunningCount = notRunningCount
		}

		return notRunningCount == 0, nil
	})
}

func verifyEventTTLInPods(ctx context.Context, kubeClient *kubernetes.Clientset, expectedMinutes int32) {
	g.By(fmt.Sprintf("Verifying event-ttl=%dm in kube-apiserver pods", expectedMinutes))

	pods, err := kubeClient.CoreV1().Pods(operatorclient.TargetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=openshift-kube-apiserver,apiserver=true",
	})
	o.Expect(err).NotTo(o.HaveOccurred())
	g.By(fmt.Sprintf("  Found %d total pods", len(pods.Items)))

	runningPodCount := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPodCount++
		}
	}
	g.By(fmt.Sprintf("  %d pods in Running state", runningPodCount))

	expectedTTL := fmt.Sprintf("%dm", expectedMinutes)
	foundExpected := false
	var actualTTLValues []string
	checkedPods := 0

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			g.By(fmt.Sprintf("  Skipping pod %s (Phase: %s)", pod.Name, pod.Status.Phase))
			continue
		}

		checkedPods++
		g.By(fmt.Sprintf("  Checking pod %s (Age: %v)", pod.Name, time.Since(pod.CreationTimestamp.Time).Round(time.Second)))

		for _, container := range pod.Spec.Containers {
			if container.Name == "kube-apiserver" {
				allArgs := append(container.Command, container.Args...)
				foundTTLArg := false

				for _, arg := range allArgs {
					if strings.HasPrefix(arg, "--event-ttl=") {
						foundTTLArg = true
						ttlValue := strings.TrimPrefix(arg, "--event-ttl=")
						actualTTLValues = append(actualTTLValues, ttlValue)

						if ttlValue == expectedTTL || ttlValue == fmt.Sprintf("%dm0s", expectedMinutes) {
							foundExpected = true
							g.By(fmt.Sprintf("    ✓ Pod %s has event-ttl=%s (matches expected)", pod.Name, ttlValue))
						} else {
							g.By(fmt.Sprintf("    ⚠ Pod %s has event-ttl=%s (expected %s)", pod.Name, ttlValue, expectedTTL))
						}
						break
					}
				}

				if !foundTTLArg {
					g.By(fmt.Sprintf("    ⚠ Pod %s does NOT have --event-ttl argument", pod.Name))
				}
			}
		}
	}

	g.By(fmt.Sprintf("  Summary: Checked %d running pods", checkedPods))
	g.By(fmt.Sprintf("  Expected: event-ttl=%s", expectedTTL))
	g.By(fmt.Sprintf("  Found values: %v", actualTTLValues))

	if !foundExpected && len(actualTTLValues) > 0 {
		o.Expect(foundExpected).To(o.BeTrue(), fmt.Sprintf("expected event-ttl=%s but found %v in pods", expectedTTL, actualTTLValues))
	} else {
		o.Expect(foundExpected).To(o.BeTrue(), fmt.Sprintf("expected event-ttl=%s not found in any running pods", expectedTTL))
	}
}
