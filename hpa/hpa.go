package hpa

import (
	"bytes"
	"fmt"
	"k8s.io/apimachinery/pkg/util/wait"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/origin/test/extended/util"
	compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// createResourceFromString applies a YAML manifest string to the cluster
func createResourceFromString(oc *exutil.CLI, namespace, manifest string) error {
	// Create a temporary file for the manifest
	tempFile, err := os.CreateTemp("", "manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempFileName := tempFile.Name()
	defer os.Remove(tempFileName)

	// Write manifest to temp file
	if _, err := tempFile.WriteString(manifest); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to write manifest to temp file: %w", err)
	}
	tempFile.Close()

	// Apply the manifest
	var applyErr error
	if namespace != "" {
		_, applyErr = oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", tempFileName, "-n", namespace).Output()
	} else {
		_, applyErr = oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", tempFileName).Output()
	}

	if applyErr != nil {
		return fmt.Errorf("failed to apply manifest: %w", applyErr)
	}

	return nil
}

// createWorkloadFromTemplate renders a template, applies it, and waits for deployment ready
// This replaces the old createWorkload() pattern with better separation of concerns
func createWorkloadFromTemplate(oc *exutil.CLI, manifest, deploymentName, namespace string, timeout time.Duration) error {
	// Apply manifest
	e2e.Logf("Applying deployment manifest for %s in namespace %s", deploymentName, namespace)
	if err := createResourceFromString(oc, namespace, manifest); err != nil {
		e2e.Logf("Failed to apply manifest: %v", err)
		return fmt.Errorf("failed to apply workload manifest: %w", err)
	}
	// Verify deployment was created
	deployCheck, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("deployment", deploymentName, "-n", namespace, "-o", "name").Output()
	if err != nil || deployCheck == "" {
		e2e.Logf("WARNING: Deployment %s was not created in namespace %s", deploymentName, namespace)
		e2e.Logf("Checking all deployments in namespace:")
		allDeploys, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("deployments", "-n", namespace).Output()
		e2e.Logf("Deployments:\n%s", allDeploys)
	} else {
		e2e.Logf("Deployment %s created successfully", deployCheck)
	}
	// Wait for deployment to be ready
	e2e.Logf("Waiting for deployment %s to become ready (timeout: %v)", deploymentName, timeout)
	if err := waitForDeploymentReady(oc, deploymentName, namespace, timeout); err != nil {
		return fmt.Errorf("deployment %s did not become ready: %w", deploymentName, err)
	}

	return nil
}

// waitForDeploymentReady polls a deployment until all replicas are ready
// Returns error if deployment doesn't become ready within the timeout
// Provides comprehensive diagnostics on failure (pod status, events, image pull errors)
func waitForDeploymentReady(oc *exutil.CLI, deploymentName, namespace string, timeout time.Duration) error {
	var lastPodStatus string
	imagePullBackOffCount := 0

	err := wait.Poll(10*time.Second, timeout, func() (bool, error) {
		output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("deployment", deploymentName,
			"-n", namespace,
			"-o", "jsonpath={.status.replicas},{.status.readyReplicas}").Output()

		if err != nil {
			// Deployment might not exist yet, keep waiting
			return false, nil
		}

		parts := strings.Split(strings.TrimSpace(output), ",")
		if len(parts) != 2 {
			return false, nil
		}

		replicas, _ := strconv.Atoi(parts[0])
		readyReplicas, _ := strconv.Atoi(parts[1])

		if replicas > 0 && replicas == readyReplicas {
			return true, nil // Success
		}

		// Check pod status for failure conditions (ImagePullBackOff, CrashLoopBackOff, etc.)
		podStatus, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pods",
			"-n", namespace,
			"-l", "app="+deploymentName,
			"-o", "jsonpath={.items[*].status.containerStatuses[*].state}").Output()

		if err == nil && podStatus != "" {
			lastPodStatus = podStatus

			// Fail fast on ImagePullBackOff (image not available or pull error)
			if strings.Contains(podStatus, "ImagePullBackOff") || strings.Contains(podStatus, "ErrImagePull") {
				imagePullBackOffCount++
				// After 3 consecutive ImagePullBackOff detections (~30s), fail fast
				if imagePullBackOffCount >= 3 {
					return false, fmt.Errorf("ImagePullBackOff detected - image cannot be pulled")
				}
			} else {
				imagePullBackOffCount = 0
			}

			// Fail fast on CrashLoopBackOff (container crashes on start)
			if strings.Contains(podStatus, "CrashLoopBackOff") {
				return false, fmt.Errorf("CrashLoopBackOff detected - container crashing on start")
			}
		}

		return false, nil // Keep waiting
	})

	// On timeout or failure, provide comprehensive diagnostics
	if err != nil {
		e2e.Logf("Deployment %s failed to become ready in namespace %s", deploymentName, namespace)
		e2e.Logf("Last pod status: %s", lastPodStatus)

		// Get pod details
		podList, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("pods",
			"-n", namespace,
			"-l", "app="+deploymentName,
			"-o", "wide").Output()
		e2e.Logf("Pod list:\n%s", podList)

		// Get pod events
		events, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("events",
			"-n", namespace,
			"--field-selector", "involvedObject.kind=Pod",
			"--sort-by", ".lastTimestamp").Output()
		e2e.Logf("Pod events:\n%s", events)

		// Get deployment status
		deployStatus, _ := oc.AsAdmin().WithoutNamespace().Run("describe").Args("deployment", deploymentName,
			"-n", namespace).Output()
		e2e.Logf("Deployment status:\n%s", deployStatus)
	}

	return err
}


func checkWorkloadCreated(oc *exutil.CLI, name, namespace string, replica_count int) bool {
	readyReplicas, err := oc.AsAdmin().WithoutNamespace().Run("get").Args(
		"deployment", name, "-n", namespace, "-o=jsonpath={.status.readyReplicas}",
	).Output()

	if err != nil {
		// oc CLI returns a plain string error, not a Kubernetes StatusError, so
		// apierrors.IsNotFound does not work here. Check the error string instead.
		if strings.Contains(err.Error(), "not found") {
			// Deployment not yet visible or already gone; success only when expecting 0 replicas.
			return replica_count == 0
		}
		// Any other error is a fatal test failure. Fail immediately to avoid getting stuck.
		e2e.Failf("Failed to get deployment %s, aborting test: %v", name, err)
		return false // Unreachable but required by compiler
	}

	// If the readyReplicas field is empty, it's treated as 0 replicas.
	if readyReplicas == "" {
		return replica_count == 0
	}

	numberOfWorkloads, err := strconv.Atoi(readyReplicas)
	if err != nil {
		// Let the poll retry on a parse error, as the output may be temporarily garbled.
		e2e.Logf("Could not parse readyReplicas count '%s' for deployment %s: %v", readyReplicas, name, err)
		return false
	}

	return numberOfWorkloads == replica_count
}

func deleteProject(oc *exutil.CLI, namespace string) {
	oc.DeleteSpecifiedNamespaceAsAdmin(namespace)
}

// RenderHPA renders the HPA Go template
func RenderHPA(config *HPAConfig) (string, error) {
	templateContent, err := GetFileContent("hpa_template.yaml")
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("hpa").Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %v", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, config)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.String(), nil
}

func NewDefaultHPAWebServerConfig() *HPAWebServerConfig {
	return &HPAWebServerConfig{
		ContainerName:      "hpawebserver",
		Replicas:           1,
		IncludeService:     true,
		IncludeTolerations: true,
		//PowerShellExe:          "pwsh.exe", //pkr -How to make this run /bin/sh?
		IncludeSecurityContext: true,
	}
}

// HPAMetric represents a single HPA metric
type HPAMetric struct {
	Name         string // Resource name (e.g., "cpu", "memory")
	AverageValue string // Average value threshold (e.g., "800m", "1Gi")
}

type HPAConfig struct {
	ResourceName               string      // HPA resource name
	Namespace                  string      // Namespace
	DeploymentName             string      // Target deployment name
	MinReplicas                int         // Minimum replicas
	MaxReplicas                int         // Maximum replicas
	Metrics                    []HPAMetric // Metrics for scaling
	StabilizationWindowSeconds int         // Stabilization window for scale down (optional)
}

// GetFileContent reads a template file from testdata/hpa/
func GetFileContent(filename string) (string, error) {
	templatePath := compat_otp.FixturePath("testdata", "hpa", filename)
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("failed to read template file %s: %v", filename, err)
	}
	return string(content), nil
}

// NewDefaultHPAConfig returns a config with defaults
func NewDefaultHPAConfig() *HPAConfig {
	return &HPAConfig{
		MinReplicas: 1,
		MaxReplicas: 5,
	}
}

func createProject(oc *exutil.CLI, namespace string) {
	exists := oc.AsAdmin().WithoutNamespace().Run("get").Args("namespace", namespace).Execute()
	if exists == nil {
		e2e.Logf("Namespace %s already exists, skipping creation", namespace)
		return
	}

	oc.CreateSpecifiedNamespaceAsAdmin(namespace)
		err := compat_otp.SetNamespacePrivileged(oc, namespace)
	o.Expect(err).NotTo(o.HaveOccurred())
}


func haveMetricsServer(oc *exutil.CLI) bool {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("apiservice", "v1beta1.metrics.k8s.io").Output()
	return err == nil && strings.Contains(output, "True")
}

func getConfigMapData(oc *exutil.CLI, cm string, dataKey string, namespace string) string {
	dataValue, err := oc.AsAdmin().WithoutNamespace().Run("get").
		Args("configmap", cm, "-o=jsonpath={.data."+dataKey+"}", "-n", namespace).Output()
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("ERROR: get cm %v -o=jsonpath={.data.%v} failed: %v", cm, dataKey, err))
	return dataValue
}

//this porting won't work -> the HPA websrever template used to be called the windows web server template, and the template actually uses windows stuff, so you can't just be naive about it.
func RenderHPAWebServerTemplate(config *HPAWebServerConfig) (string, error) {
	templateContent, err := GetFileContent("HPA_web_server_template.yaml")
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("HPA_webserver").Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %v", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, config)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.String(), nil
}

var (
//	buildPruningBaseDir = compat_otp.FixturePath("testdata", "node")
	HPATestConfigMap    = "HPA-test-configmap"
	defaultNamespace    = "hpa-test"
	HPAWorkloads        = "webserver"
)

var _ = g.Describe("AUTOSCALE - TestNumber99999 - HPA memory-based scaling (smoke)test", func() {
	defer g.GinkgoRecover()

	oc := compat_otp.NewCLIWithoutNamespace("default")

	// author: prozehna@redhat.com
	g.It("Author:prozehna-99999-Horizontal Pod Autoscaler Smoketest", func() {
		if !haveMetricsServer(oc) {
			g.Skip("metrics-server is required for HPA testing")
		}

		namespace := "hpa-99999"
		defer deleteProject(oc, namespace)

		g.By("Creating test namespace")
		createProject(oc, namespace)

		g.By("Fetching image")
		LinuxImage := getConfigMapData(oc, HPATestConfigMap, "primary_container_image", defaultNamespace)

		// Create and render web server deployment using Go template
		config := NewDefaultHPAWebServerConfig()
		config.ImageName = LinuxImage
		config.ResourceLimits = "200m" // Set resource requests for HPA to calculate replica count

		manifest, err := RenderHPAWebServerTemplate(config)
		o.Expect(err).NotTo(o.HaveOccurred())

		err = createWorkloadFromTemplate(oc, manifest, HPAWorkloads, namespace, 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred())

		// Memory HPA test
		g.By("Creating memory-based HPA")
		memoryHPA := NewDefaultHPAConfig()
		memoryHPA.ResourceName = "hpa-resource-metrics-memory"
		memoryHPA.Namespace = namespace
		memoryHPA.DeploymentName = HPAWorkloads
		memoryHPA.Metrics = []HPAMetric{{Name: "memory", AverageValue: "40Mi"}}
		memoryHPA.StabilizationWindowSeconds = 20
		memoryManifest, err := RenderHPA(memoryHPA)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createResourceFromString(oc, namespace, memoryManifest)
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create memory HPA")
		defer oc.AsAdmin().WithoutNamespace().Run("delete").Args("hpa", "hpa-resource-metrics-memory", "-n", namespace, "--ignore-not-found").Execute()

		g.By("Verifying HPA scales up deployment")
		err = wait.Poll(10*time.Second, 5*time.Minute, func() (bool, error) {
			msg, _ := oc.AsAdmin().WithoutNamespace().Run("get").
				Args("deployment", HPAWorkloads, "-o=jsonpath={.status.readyReplicas}", "-n", namespace).Output()
			numberOfWorkloads, _ := strconv.Atoi(msg)
			return numberOfWorkloads > 1, nil
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "Deployment did not scale up")

		g.By("Patching memory HPA to trigger scale down")
		_, err = oc.WithoutNamespace().Run("patch").Args(
			"hpa", "hpa-resource-metrics-memory",
			"-n", namespace,
			"--type=merge",
			"--patch", `{"spec":{"metrics":[{"resource":{"target":{"type":"AverageValue","averageValue":"150Mi"},"name":"memory"},"type":"Resource"}]}}`,
		).Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to patch memory HPA")

		g.By("Removing memory HPA")
		oc.AsAdmin().WithoutNamespace().Run("delete").Args("hpa", "hpa-resource-metrics-memory", "-n", namespace, "--ignore-not-found").Execute()
		checkWorkloadCreated(oc, HPAWorkloads, namespace, 1)

		g.By("Verifying HPA scales up deployment")
		err = wait.Poll(10*time.Second, 5*time.Minute, func() (bool, error) {
			msg, _ := oc.AsAdmin().WithoutNamespace().Run("get").
				Args("deployment", HPAWorkloads, "-o=jsonpath={.status.readyReplicas}", "-n", namespace).Output()
			numberOfWorkloads, _ := strconv.Atoi(msg)
			return numberOfWorkloads > 1, nil
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "Deployment did not scale up")

		g.By("Patching memory HPA to trigger scale down")
		_, err = oc.WithoutNamespace().Run("patch").Args(
			"hpa", "hpa-resource-metrics-memory",
			"-n", namespace,
			"--type=merge",
			"--patch", `{"spec":{"metrics":[{"resource":{"target":{"type":"AverageValue","averageValue":"150Mi"},"name":"memory"},"type":"Resource"}]}}`,
		).Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to patch memory HPA")

		g.By("Removing memory HPA")
		oc.AsAdmin().WithoutNamespace().Run("delete").Args("hpa", "hpa-resource-metrics-memory", "-n", namespace, "--ignore-not-found").Execute()
		checkWorkloadCreated(oc, HPAWorkloads, namespace, 1)

		// CPU HPA test
		g.By("Creating CPU-based HPA")
		cpuHPA := NewDefaultHPAConfig()
		cpuHPA.ResourceName = "hpa-resource-metrics-cpu"
		cpuHPA.Namespace = namespace
		cpuHPA.DeploymentName = HPAWorkloads
		cpuHPA.Metrics = []HPAMetric{{Name: "cpu", AverageValue: "10m"}}
		cpuHPA.StabilizationWindowSeconds = 20
		cpuManifest, err := RenderHPA(cpuHPA)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createResourceFromString(oc, namespace, cpuManifest)
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create CPU HPA")
		defer oc.AsAdmin().WithoutNamespace().Run("delete").Args("hpa", "hpa-resource-metrics-cpu", "-n", namespace, "--ignore-not-found").Execute()

		g.By("Verifying HPA scales up deployment")
		err = wait.Poll(10*time.Second, 5*time.Minute, func() (bool, error) {
			msg, _ := oc.AsAdmin().WithoutNamespace().Run("get").
				Args("deployment", HPAWorkloads, "-o=jsonpath={.status.readyReplicas}", "-n", namespace).Output()
			numberOfWorkloads, _ := strconv.Atoi(msg)
			return numberOfWorkloads > 1, nil
		})
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Patching CPU HPA to trigger scale down")
		_, err = oc.WithoutNamespace().Run("patch").Args(
			"hpa", "hpa-resource-metrics-cpu",
			"-n", namespace,
			"--type=merge",
			"--patch", `{"spec":{"metrics":[{"resource":{"target":{"type":"AverageValue","averageValue":"500m"},"name":"cpu"},"type":"Resource"}]}}`,
		).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Removing CPU HPA")
		oc.WithoutNamespace().Run("delete").Args("hpa", "hpa-resource-metrics-cpu", "-n", namespace).Execute()

		// verify the deployment is scaled down
		checkWorkloadCreated(oc, HPAWorkloads, namespace, 1)
	})

}
